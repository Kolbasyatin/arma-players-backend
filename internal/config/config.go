package config

import (
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
)

type Config struct {
	HTTPAddr     string        `env:"HTTP_ADDR" envDefault:":8080"`
	DatabaseURL  string        `env:"DATABASE_URL"`
	PollInterval time.Duration `env:"POLL_INTERVAL" envDefault:"1m"`
	TokenURL     string        `env:"TOKEN_URL,required"`
	Bohemia      Bohemia       `envPrefix:"BOHEMIA_"`
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

func Load() (Config, error) {
	_ = godotenv.Load(".env")

	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}
