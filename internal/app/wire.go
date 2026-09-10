// Package app собирает зависимости из конфигурации. Единственное место, где
// config превращается в конкретные клиенты; cmd/* только вызывают эти функции.
package app

import (
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"armaplayers/internal/bohemia"
	"armaplayers/internal/catalog"
	"armaplayers/internal/config"
	"armaplayers/internal/presence"
	"armaplayers/internal/storage/postgres"
	"armaplayers/internal/token"
	"armaplayers/internal/tracking"
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

// NewTokenProvider создаёт провайдер BI-токена (кэш поверх HTTP-источника).
func NewTokenProvider(cfg config.Config) *token.Provider {
	return token.NewProvider(token.NewHTTPSource(cfg.TokenURL, nil), 0)
}

// Services — все фоновые компоненты observer, собранные на общих клиенте, токене и пуле.
type Services struct {
	Scanner *catalog.Scanner
	Tracker *tracking.Tracker
}

func NewServices(cfg config.Config, pool *pgxpool.Pool, log *slog.Logger) Services {
	client := NewBohemiaClient(cfg.Bohemia)
	tokens := NewTokenProvider(cfg)

	scanner := catalog.NewScanner(client, tokens, postgres.NewCatalogRepo(pool), cfg.LobbyPageSize, cfg.RawRetention,
		catalog.TrackingRules{
			ManualHosts:    cfg.TrackHostAddresses,
			AutoMinPlayers: cfg.TrackAutoMinPlayers,
			AutoMaxServers: cfg.TrackAutoMaxServers,
			AutoLookback:   cfg.TrackAutoLookback,
		}, log)

	pres := presence.NewService(postgres.NewPresenceStore(pool), presence.Config{
		AbsentConfirmations: cfg.PresenceAbsentConfirmations,
		MaxGap:              cfg.PresenceMaxGap,
	})
	tracker := tracking.NewTracker(client, tokens, postgres.NewTrackingRepo(pool), pres, tracking.Config{
		Interval:     cfg.PollInterval,
		Concurrency:  cfg.PollConcurrency,
		RawRetention: cfg.RawRetention,
	}, log)

	return Services{Scanner: scanner, Tracker: tracker}
}
