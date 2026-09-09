package bohemia

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"
)

type Config struct {
	BaseURL       string // https://api-ar-game.bistudio.com/game-api/api/v1.0
	PlatformID    string // ReforgerSteam
	ClientVersion string // 1.8.0
	UserAgent     string // Arma Reforger/1.8.0.13 (Client; Windows)
}

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

type listPlayersRequest struct {
	RoomID        string `json:"roomId"`
	AccessToken   string `json:"accessToken"`
	PlatformID    string `json:"platformId"`
	ClientVersion string `json:"clientVersion"`
}

func (c *Client) ListPlayers(ctx context.Context, accessToken, roomID string) (ListPlayersResponse, error) {
	body, err := json.Marshal(listPlayersRequest{
		RoomID:        roomID,
		AccessToken:   accessToken,
		PlatformID:    c.cfg.PlatformID,
		ClientVersion: c.cfg.ClientVersion,
	})
	if err != nil {
		return ListPlayersResponse{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.BaseURL+"/lobby/rooms/listPlayers", bytes.NewReader(body))
	if err != nil {
		return ListPlayersResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.cfg.UserAgent)

	resp, err := c.http.Do(req)

	if err != nil {
		return ListPlayersResponse{}, classifyTransport("listPlayers", err)
	}

	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return ListPlayersResponse{}, classifyStatus("listPlayers", resp.StatusCode)
	}

	var out ListPlayersResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return ListPlayersResponse{}, &Error{Kind: KindInvalidJSON, Op: "listPlayers", HTTPStatus: resp.StatusCode, Err: err}
	}
	return out, nil
}
