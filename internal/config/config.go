package config

import (
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
)

// Config — вся конфигурация observer из переменных окружения (12-factor). Дефолты — рабочие
// значения для прода; локально переопределяются через .env.
type Config struct {
	DatabaseURL string `env:"DATABASE_URL"`
	// TokenURL — GET-эндпоинт сервиса arma-reforger-hz, отдающего BI access token.
	TokenURL string `env:"TOKEN_URL,required"`
	// HTTPAddr — адрес служебного HTTP: /health и /observation-status (AGENTS §24).
	HTTPAddr string `env:"HTTP_ADDR" envDefault:":8081"`

	// Полный скан лобби (AGENTS §13): интервал, пауза после ошибки, размер страницы.
	LobbyScanInterval time.Duration `env:"LOBBY_SCAN_INTERVAL" envDefault:"24h"`
	LobbyScanRetry    time.Duration `env:"LOBBY_SCAN_RETRY" envDefault:"15m"`
	LobbyPageSize     int           `env:"LOBBY_PAGE_SIZE" envDefault:"500"` // 500 проверено на живом API

	// Минутный опрос отслеживаемых серверов (AGENTS §14).
	PollInterval    time.Duration `env:"POLL_INTERVAL" envDefault:"1m"`
	PollConcurrency int           `env:"POLL_CONCURRENCY" envDefault:"4"`

	// Выбор серверов для опроса (ADR 0008): ручной список адресов и авто-правило по онлайну.
	TrackHostAddresses  []string `env:"TRACK_HOST_ADDRESSES" envSeparator:","`
	TrackAutoMinPlayers int      `env:"TRACK_AUTO_MIN_PLAYERS" envDefault:"0"` // 0 — авто выключено
	TrackAutoMaxServers int      `env:"TRACK_AUTO_MAX_SERVERS" envDefault:"10"`
	// TrackAutoLookback — окно, за которое берётся пиковый онлайн сервера для авто-правила.
	TrackAutoLookback time.Duration `env:"TRACK_AUTO_LOOKBACK" envDefault:"168h"`

	// Присутствие (ADR 0005): подтверждений отсутствия для выхода и допустимый разрыв данных.
	PresenceAbsentConfirmations int           `env:"PRESENCE_ABSENT_CONFIRMATIONS" envDefault:"2"`
	PresenceMaxGap              time.Duration `env:"PRESENCE_MAX_GAP" envDefault:"15m"`

	// Сырые ответы Bohemia (AGENTS §12): срок хранения и период чистки.
	RawRetention      time.Duration `env:"RAW_RETENTION" envDefault:"336h"`
	RetentionInterval time.Duration `env:"RETENTION_INTERVAL" envDefault:"1h"`

	Bohemia Bohemia `envPrefix:"BOHEMIA_"`
}

// Bohemia — протокольные константы lobby API. Сняты с клиента 1.8.0.13 и меняются
// с патчами игры, поэтому переопределяются через окружение без пересборки (AGENTS §3).
type Bohemia struct {
	BaseURL        string `env:"BASE_URL" envDefault:"https://api-ar-game.bistudio.com/game-api/api/v1.0"`
	PlatformID     string `env:"PLATFORM_ID" envDefault:"ReforgerSteam"`
	GameClientType string `env:"GAME_CLIENT_TYPE" envDefault:"PLATFORM_PC"`
	ClientVersion  string `env:"CLIENT_VERSION" envDefault:"1.8.0"`
	UserAgent      string `env:"USER_AGENT" envDefault:"Arma Reforger/1.8.0.13 (Client; Windows)"`
}

// Load читает .env (если есть) и переменные окружения. Реальное окружение приоритетнее файла.
func Load() (Config, error) {
	_ = godotenv.Load(".env")

	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
