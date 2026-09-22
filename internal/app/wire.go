// Package app собирает зависимости из конфигурации. Единственное место, где
// config превращается в конкретные клиенты; cmd/* только вызывают эти функции.
package app

import (
	"context"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"

	"armaplayers/internal/bohemia"
	"armaplayers/internal/catalog"
	"armaplayers/internal/config"
	"armaplayers/internal/httpapi"
	"armaplayers/internal/presence"
	"armaplayers/internal/steam"
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

// steamDossier связывает сервис Steam с его хранилищем: httpapi нужен один метод,
// а сервису — переданный store, потому что фоновому циклу он не нужен.
type steamDossier struct {
	service *steam.Service
	store   steam.DossierStore
}

func (d steamDossier) Dossier(ctx context.Context, playerID int64) (steam.Dossier, error) {
	return d.service.Dossier(ctx, d.store, playerID)
}

func (d steamDossier) DossierBySteamID(ctx context.Context, steamID string) (steam.Dossier, error) {
	return d.service.DossierBySteamID(ctx, d.store, steamID)
}

// Services — все фоновые компоненты observer, собранные на общих клиенте, токене и пуле.
type Services struct {
	Scanner *catalog.Scanner
	Tracker *tracking.Tracker
	// Steam — nil, если STEAM_GATEWAY_URL не задан: тема необязательная, и без шлюза
	// observer обязан работать ровно как раньше.
	Steam *steam.Service
	// Dossier — реализация httpapi.DossierProvider; nil вместе со Steam.
	Dossier httpapi.DossierProvider
}

func NewServices(cfg config.Config, pool *pgxpool.Pool, log *slog.Logger) Services {
	client := NewBohemiaClient(cfg.Bohemia)
	tokens := NewTokenProvider(cfg)

	raw := cfg.Raw()
	scanner := catalog.NewScanner(client, tokens, postgres.NewCatalogRepo(pool), cfg.LobbyPageSize, cfg.RawRetention,
		raw == config.RawStoreScan || raw == config.RawStoreAll,
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
		StoreRaw:     raw == config.RawStoreAll,
	}, log)

	services := Services{Scanner: scanner, Tracker: tracker}

	if cfg.SteamGatewayURL != "" {
		repo := postgres.NewSteamRepo(pool)
		services.Steam = steam.NewService(steam.NewClient(cfg.SteamGatewayURL, nil), repo, log)
		services.Dossier = steamDossier{service: services.Steam, store: repo}
	}

	return services
}
