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
	// HTTPAddr — адрес HTTP: служебные /health, /observation-status и REST API для бота/интерфейса.
	HTTPAddr string `env:"HTTP_ADDR" envDefault:":8081"`
	// APIToken — Bearer-токен для REST API. Пустой — API выключен (служебные маршруты работают).
	APIToken string `env:"API_TOKEN"`

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

	// Сырые ответы Bohemia (AGENTS §12). Всё содержимое ответов разложено по таблицам, поэтому
	// сырьё нужно только для отладки формата — и по умолчанию НЕ хранится: на проде оно занимало
	// 5,7 ГБ из 11, больше всех остальных данных вместе взятых.
	//   off  — не хранить вовсе (по умолчанию);
	//   scan — только ответы суточного скана лобби (~16 МБ на скан), их хватает для изучения формата;
	//   all  — плюс ответы минутного опроса: десятки гигабайт, включать только на время отладки.
	RawStore          string        `env:"RAW_STORE" envDefault:"off"`
	RawRetention      time.Duration `env:"RAW_RETENTION" envDefault:"48h"`
	RetentionInterval time.Duration `env:"RETENTION_INTERVAL" envDefault:"1h"`

	// Данные Steam об «избранных» игроках (схема steam). Ключ Valve живёт в arma-reforger-hz,
	// сюда приходят уже собранные ответы.
	//   SteamGatewayURL — корень hz, например http://arma-reforger-hz:8080. Пусто — тема выключена.
	//   SteamRefreshInterval — как часто просыпается обход списка;
	//   SteamBatch — сколько игроков за один проход, чтобы не выгрести квоту разом.
	//   SteamActiveWindow — за какой срок игрок должен появиться на наших серверах, чтобы
	//     считаться активным; активных обновляем часто, остальных редко.
	SteamGatewayURL      string        `env:"STEAM_GATEWAY_URL"`
	SteamRefreshInterval time.Duration `env:"STEAM_REFRESH_INTERVAL" envDefault:"1h"`
	SteamActiveWindow    time.Duration `env:"STEAM_ACTIVE_WINDOW" envDefault:"168h"`
	SteamActiveStaleness time.Duration `env:"STEAM_ACTIVE_STALENESS" envDefault:"24h"`
	SteamIdleStaleness   time.Duration `env:"STEAM_IDLE_STALENESS" envDefault:"168h"`
	SteamBatch           int           `env:"STEAM_BATCH" envDefault:"50"`

	// Логи: JSON в stdout всегда; LOG_FILE добавляет файл с ротацией (размер в МБ, число копий, дни).
	LogFile       string `env:"LOG_FILE"`
	LogMaxSizeMB  int    `env:"LOG_MAX_SIZE_MB" envDefault:"50"`
	LogMaxBackups int    `env:"LOG_MAX_BACKUPS" envDefault:"10"`
	LogMaxAgeDays int    `env:"LOG_MAX_AGE_DAYS" envDefault:"30"`

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
// RawStoreMode — что из сырых ответов сохранять.
type RawStoreMode string

const (
	RawStoreOff  RawStoreMode = "off"
	RawStoreScan RawStoreMode = "scan"
	RawStoreAll  RawStoreMode = "all"
)

// Raw возвращает режим хранения сырья; неизвестное значение трактуется как off,
// потому что «случайно включить запись десятков гигабайт» хуже, чем «случайно не записать отладку».
func (c Config) Raw() RawStoreMode {
	switch RawStoreMode(c.RawStore) {
	case RawStoreScan:
		return RawStoreScan
	case RawStoreAll:
		return RawStoreAll
	default:
		return RawStoreOff
	}
}

func Load() (Config, error) {
	_ = godotenv.Load(".env")

	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
