package config

import (
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
)

type Config struct {
	HTTPAddr     string        `env:"HTTP_ADDR" envDefault:":8080"`
	DatabaseURL  string        `env:"DATABASE_URL,required"`
	PollInterval time.Duration `env:"POLL_INTERVAL" envDefault:"1m"`
}

func Load() (Config, error) {
	_ = godotenv.Load(".env")
	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return Config{}, err
	}

	return cfg, nil
}
