// Package presence — diff engine (ADR 0005): сравнивает список игроков с предыдущим состоянием
// и ведёт идентичности, алиасы, сессии присутствия и очереди, порождая derived-события.
// Неуспешный poll сюда не попадает вовсе — состояние меняют только успешные наблюдения.
package presence

import (
	"context"
	"time"

	"armaplayers/internal/bohemia"
)

// SessionStatus — состояние сессии присутствия или очереди.
type SessionStatus string

const (
	StatusOnline        SessionStatus = "ONLINE"
	StatusSuspectedGone SessionStatus = "SUSPECTED_GONE"
	StatusClosedLeft    SessionStatus = "CLOSED_LEFT"
	StatusClosedDataGap SessionStatus = "CLOSED_DATA_GAP"
)

// QueueResult — чем закончилось ожидание в очереди.
type QueueResult string

const (
	QueueJoinedServer QueueResult = "JOINED_SERVER"
	QueueLeft         QueueResult = "LEFT_QUEUE"
	QueueUnknown      QueueResult = "UNKNOWN"
)

// EventType — derived-событие.
type EventType string

const (
	EventPlayerJoinedServer    EventType = "PLAYER_JOINED_SERVER"
	EventPlayerLeftServer      EventType = "PLAYER_LEFT_SERVER"
	EventPlayerEnteredQueue    EventType = "PLAYER_ENTERED_QUEUE"
	EventPlayerLeftQueue       EventType = "PLAYER_LEFT_QUEUE"
	EventPlayerNicknameChanged EventType = "PLAYER_NICKNAME_CHANGED"
)

// Session — сессия присутствия на сервере или ожидания в очереди (одна форма для обеих).
type Session struct {
	ID                 int64
	PlayerID           int64
	ServerID           int64
	FirstSeenAt        time.Time
	LastSeenAt         time.Time
	FirstKnownAbsentAt *time.Time
	EndedAt            *time.Time
	Status             SessionStatus
	AbsentPolls        int
	StartupReplay      bool
	QueueResult        QueueResult // только для очереди
}

// Event — строка domain_event.
type Event struct {
	Type          EventType
	OccurredAt    time.Time
	PlayerID      int64
	ServerID      int64
	SessionID     *int64
	Payload       map[string]any
	StartupReplay bool
	AfterDataGap  bool
}

// PlayerRef — результат upsert игрока.
type PlayerRef struct {
	PlayerID         int64
	IsNew            bool
	PreviousNickname string // "" для нового игрока
}

// Tx — операции хранилища внутри одной транзакции. Реализация в storage/postgres.
type Tx interface {
	UpsertPlayer(ctx context.Context, p bohemia.Player, seenAt time.Time) (PlayerRef, error)
	LastSuccessfulListPlayersAt(ctx context.Context, serverID int64) (time.Time, bool, error)

	OpenPresenceSessions(ctx context.Context, serverID int64) ([]Session, error)
	InsertPresenceSession(ctx context.Context, s Session) (int64, error)
	UpdatePresenceSession(ctx context.Context, s Session) error

	OpenQueueSessions(ctx context.Context, serverID int64) ([]Session, error)
	InsertQueueSession(ctx context.Context, s Session) (int64, error)
	UpdateQueueSession(ctx context.Context, s Session) error

	AppendEvent(ctx context.Context, e Event) error
}

// Store открывает транзакцию и выполняет fn внутри неё; ошибка fn откатывает всё.
type Store interface {
	WithTx(ctx context.Context, fn func(tx Tx) error) error
}

// Observation — успешный ответ listPlayers, отнесённый к серверу.
type Observation struct {
	ServerID      int64
	ObservedAt    time.Time
	Connected     []bohemia.Player
	Queue         []bohemia.Player
	StartupReplay bool // первый успешный poll сервера после старта процесса
}

// Result — что изменилось после применения наблюдения.
type Result struct {
	Players         int
	Queued          int
	NewPlayers      int
	Joined          int
	Left            int
	SuspectedGone   int
	DataGapClosed   int
	QueueEntered    int
	QueueClosed     int
	NicknameChanges int
	AfterDataGap    bool
}

// Config — пороги алгоритма; все конфигурируемые (AGENTS §7.2).
type Config struct {
	AbsentConfirmations int           // сколько успешных poll подряд без игрока = выход; default 2
	MaxGap              time.Duration // разрыв без успешных poll, после которого сессии закрываются как DATA_GAP; default 15m
}

func (c Config) withDefaults() Config {
	if c.AbsentConfirmations <= 0 {
		c.AbsentConfirmations = 2
	}
	if c.MaxGap <= 0 {
		c.MaxGap = 15 * time.Minute
	}
	return c
}
