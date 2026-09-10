// Package app собирает зависимости из конфигурации. Единственное место, где
// config превращается в конкретные клиенты; cmd/* только вызывают эти функции.
package app

import (
	"armaplayers/internal/bohemia"
	"armaplayers/internal/config"
	"armaplayers/internal/token"
)

// NewBohemiaClient создаёт клиент lobby API из протокольных констант конфига.
func NewBohemiaClient(cfg config.Bohemia) *bohemia.Client {
	return bohemia.New(bohemia.Config{
		BaseURL:        cfg.BaseURL,
		PlatformID:     cfg.PlatformID,
		GameClientType: cfg.GameClientType,
		ClientVersion:  cfg.ClientVersion,
		UserAgent:      cfg.UserAgent,
	}, nil)
}

// NewTokenProvider создаёт провайдер BI-токена (кэш поверх HTTP-источника) поверх сервиса arma-reforger-hz.
func NewTokenProvider(cfg config.Config) *token.Provider {
	return token.NewProvider(token.NewHTTPSource(cfg.TokenURL, nil), 0)
}
