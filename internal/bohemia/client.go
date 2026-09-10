package bohemia

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"time"
)

// Config — протокольные константы Bohemia API. Меняются между версиями игры,
// поэтому приходят из конфигурации, а не зашиты в код (AGENTS §3).
type Config struct {
	BaseURL        string // https://api-ar-game.bistudio.com/game-api/api/v1.0
	PlatformID     string // ReforgerSteam
	GameClientType string // PLATFORM_PC
	ClientVersion  string // 1.8.0
	UserAgent      string // Arma Reforger/1.8.0.13 (Client; Windows)
}

// Client — HTTP-клиент к lobby API. Безопасен для конкурентного использования.
type Client struct {
	cfg  Config
	http *http.Client
}

func New(cfg Config, httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{cfg: cfg, http: httpClient}
}

// Пути эндпоинтов относительно Config.BaseURL (AGENTS §3.1–3.2).
const (
	pathSearchRooms = "/lobby/rooms/search"
	pathListPlayers = "/lobby/rooms/listPlayers"
)

// maxBodySize ограничивает чтение тела ответа: полный скан лобби — сотни комнат,
// но не мегабайты; защита от бесконечного/испорченного ответа.
const maxBodySize = 8 << 20 // 8 MiB

type listPlayersRequest struct {
	RoomID        string `json:"roomId"`
	AccessToken   string `json:"accessToken"`
	PlatformID    string `json:"platformId"`
	ClientVersion string `json:"clientVersion"`
}

// ListPlayers возвращает игроков внутри комнаты и в очереди на вход.
func (c *Client) ListPlayers(ctx context.Context, accessToken, roomID string) (ListPlayersResponse, error) {
	const op = "listPlayers"

	var out ListPlayersResponse
	raw, err := c.postJSON(ctx, op, pathListPlayers, listPlayersRequest{
		RoomID:        roomID,
		AccessToken:   accessToken,
		PlatformID:    c.cfg.PlatformID,
		ClientVersion: c.cfg.ClientVersion,
	}, &out)
	if err != nil {
		return ListPlayersResponse{}, err
	}
	out.Raw = raw
	return out, nil
}

// RoomSearch — параметры поиска. Пустые строки означают «без фильтра».
type RoomSearch struct {
	HostAddress string // точное совпадение ip:port
	Text        string // подстрока в имени сервера
	From        int    // смещение пагинации
	Limit       int    // размер страницы; 0 → 50
	Lightweight bool   // true — урезанный ответ (для полного скана), false — все поля
}

// searchRoomsRequest — минимальное тело, которое принимает backend (снято с живого клиента 1.8.0.10).
// ascendent обязателен: без него backend отвечает InvalidInput.
type searchRoomsRequest struct {
	HostAddress      string `json:"hostAddress"`
	Text             string `json:"text"`
	Order            string `json:"order"`
	Ascendent        bool   `json:"ascendent"`
	GameClientFilter string `json:"gameClientFilter"`
	AccessToken      string `json:"accessToken"`
	ClientVersion    string `json:"clientVersion"`
	PlatformID       string `json:"platformId"`
	GameClientType   string `json:"gameClientType"`
	Lightweight      bool   `json:"lightweight"`
	From             int    `json:"from"`
	Limit            int    `json:"limit"`
	PingValues       []int  `json:"pingValues"`
}

// SearchRooms ищет комнаты в публичном лобби. Используется и для полного скана каталога
// (постранично, Lightweight=true), и для резолюции текущего roomId сервера по HostAddress.
func (c *Client) SearchRooms(ctx context.Context, accessToken string, q RoomSearch) (SearchRoomsResponse, error) {
	const op = "searchRooms"

	limit := q.Limit
	if limit <= 0 {
		limit = 50
	}

	var out SearchRoomsResponse
	raw, err := c.postJSON(ctx, op, pathSearchRooms, searchRoomsRequest{
		HostAddress:      q.HostAddress,
		Text:             q.Text,
		Order:            "PlayerCount",
		Ascendent:        false,
		GameClientFilter: "AnyCompatible",
		AccessToken:      accessToken,
		ClientVersion:    c.cfg.ClientVersion,
		PlatformID:       c.cfg.PlatformID,
		GameClientType:   c.cfg.GameClientType,
		Lightweight:      q.Lightweight,
		From:             q.From,
		Limit:            limit,
		PingValues:       []int{}, // nil-слайс сериализуется как null, backend ждёт []
	}, &out)
	if err != nil {
		return SearchRoomsResponse{}, err
	}
	out.Raw = raw
	return out, nil
}

// postJSON — общий путь всех вызовов: сериализовать тело, выполнить POST, классифицировать
// ошибку, прочитать и разобрать ответ в out. Возвращает сырое тело для retention.
func (c *Client) postJSON(ctx context.Context, op, path string, reqBody any, out any) ([]byte, error) {
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, &Error{Kind: KindInternal, Op: op, Err: err}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, &Error{Kind: KindInternal, Op: op, Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.cfg.UserAgent)

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, classifyTransport(op, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, classifyStatus(op, resp.StatusCode, body)
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
	if err != nil {
		return nil, classifyTransport(op, err)
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return nil, &Error{Kind: KindInvalidJSON, Op: op, HTTPStatus: resp.StatusCode, Err: err}
	}
	return raw, nil
}

// SearchAllRooms обходит всё лобби страницами по pageSize и отдаёт каждую страницу в visit.
// Останавливается, когда страница пришла короче pageSize или пустой, либо visit вернул ошибку.
// totalCount между страницами плавает (серверы приходят и уходят), поэтому на него не полагаемся.
func (c *Client) SearchAllRooms(ctx context.Context, accessToken string, pageSize int, visit func(page SearchRoomsResponse) error) error {
	if pageSize <= 0 {
		pageSize = 500 // проверено на живом API 2026-09-10: принимается, ~3.7 MiB на страницу
	}
	for from := 0; ; from += pageSize {
		page, err := c.SearchRooms(ctx, accessToken, RoomSearch{From: from, Limit: pageSize, Lightweight: true})
		if err != nil {
			return err
		}
		if len(page.Rooms) == 0 {
			return nil
		}
		if err := visit(page); err != nil {
			return err
		}
		if len(page.Rooms) < pageSize {
			return nil
		}
	}
}
