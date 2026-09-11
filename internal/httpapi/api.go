package httpapi

import (
	"context"
	"encoding/json"
	"time"
)

// Контракт REST API для внешних потребителей (телеграм-бот, будущий web). Описание — docs/api.md.
// Ответы — JSON, snake_case, время в RFC 3339 UTC. Все, кроме /health, требуют Authorization: Bearer <API_TOKEN>.

// Event — строка ленты событий. Курсор потребителя — максимальный id из ответа.
type Event struct {
	ID              int64           `json:"id"`
	Type            string          `json:"type"`
	OccurredAt      time.Time       `json:"occurred_at"`
	PlayerID        int64           `json:"player_id"`
	BohemiaUserID   string          `json:"bohemia_user_id"`
	Nickname        string          `json:"nickname"` // ник на момент события (из сессии), иначе текущий
	ServerID        *int64          `json:"server_id"`
	ServerName      string          `json:"server_name,omitempty"`
	SessionID       *int64          `json:"session_id"`
	DurationSeconds *int64          `json:"duration_seconds"` // для *_LEFT_*: сколько длилась сессия
	Payload         json.RawMessage `json:"payload"`          // детали события: old/new ник, result очереди
	StartupReplay   bool            `json:"startup_replay"`
	AfterDataGap    bool            `json:"after_data_gap"`
}

// EventsQuery — параметры GET /events.
type EventsQuery struct {
	After         int64    // id, после которого отдавать; 0 — с начала
	PlayerIDs     []int64  // фильтр по игрокам; пусто — все
	Types         []string // фильтр по типам; пусто — все
	Limit         int      // 1..1000, default 200
	IncludeReplay bool     // отдавать ли события startup_replay (по умолчанию нет)
}

// ServerRef — короткая ссылка на сервер.
type ServerRef struct {
	ID          int64      `json:"id"`
	Name        string     `json:"name"`
	HostAddress string     `json:"host_address"`
	Since       *time.Time `json:"since,omitempty"` // для online: с какого момента на сервере
}

// PlatformID — платформенный аккаунт игрока.
type PlatformID struct {
	Type   string    `json:"type"` // PLATFORM_PC | PLATFORM_PSN | PLATFORM_XBL
	ID     string    `json:"id"`   // SteamID64 / PSN id / XBL id
	LastAt time.Time `json:"last_seen_at"`
}

// PlayerSummary — карточка игрока для поиска и /players/{id}.
type PlayerSummary struct {
	PlayerID        int64        `json:"player_id"`
	BohemiaUserID   string       `json:"bohemia_user_id"`
	CurrentNickname string       `json:"current_nickname"`
	Aliases         []string     `json:"aliases"`
	Platforms       []PlatformID `json:"platforms"`
	FirstSeenAt     time.Time    `json:"first_seen_at"`
	LastSeenAt      time.Time    `json:"last_seen_at"`
	Online          *ServerRef   `json:"online"`      // где сейчас (открытая сессия), null если оффлайн
	LastServer      *ServerRef   `json:"last_server"` // где был последний раз
	SessionsTotal   int64        `json:"sessions_total"`
}

// Session — визит игрока на сервер.
type Session struct {
	ID              int64      `json:"id"`
	ServerID        int64      `json:"server_id"`
	ServerName      string     `json:"server_name"`
	Nickname        string     `json:"nickname"`
	FirstSeenAt     time.Time  `json:"first_seen_at"`
	LastSeenAt      time.Time  `json:"last_seen_at"`
	EndedAt         *time.Time `json:"ended_at"`
	Status          string     `json:"status"`
	DurationSeconds int64      `json:"duration_seconds"`
}

// ServerSummary — сервер каталога с текущим состоянием.
type ServerSummary struct {
	ID             int64      `json:"id"`
	Name           string     `json:"name"`
	HostAddress    string     `json:"host_address"`
	Active         bool       `json:"active"`
	Tracked        bool       `json:"tracked"`
	TrackingSource string     `json:"tracking_source,omitempty"`
	Players        *int       `json:"players"`
	PlayerLimit    *int       `json:"player_limit"`
	Queue          *int       `json:"queue"`
	ObservedAt     *time.Time `json:"observed_at"` // момент последнего снимка
	LastSeenAt     time.Time  `json:"last_seen_at"`
}

// Store — данные для API. Реализация — storage/postgres.APIRepo.
type Store interface {
	Events(ctx context.Context, q EventsQuery) ([]Event, error)
	EventsHead(ctx context.Context) (int64, error)
	// SearchPlayers: fuzzy=true — точных совпадений не нашлось, вернулись похожие по написанию.
	SearchPlayers(ctx context.Context, nick string, limit int) (players []PlayerSummary, fuzzy bool, err error)
	PlayersByIDs(ctx context.Context, ids []int64) ([]PlayerSummary, error)
	Player(ctx context.Context, id int64) (PlayerSummary, bool, error)
	PlayerSessions(ctx context.Context, id int64, limit int) ([]Session, error)
	Servers(ctx context.Context, trackedOnly bool, nameFilter string) ([]ServerSummary, error)
}
