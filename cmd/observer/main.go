// observer — основной сервис: каталог серверов, опрос игроков, сессии, события.
// Живёт постоянно: фоновые циклы под одним контекстом, останавливается по SIGINT/SIGTERM.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"golang.org/x/sync/errgroup"

	"armaplayers/internal/app"
	"armaplayers/internal/config"
	"armaplayers/internal/httpapi"
	"armaplayers/internal/storage/postgres"
)

func main() {
	if err := run(); err != nil {
		slog.Error("observer failed", "err", err)
		os.Exit(1)
	}
}

func run() error {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}
	if cfg.DatabaseURL == "" {
		return errors.New("config: DATABASE_URL is required")
	}

	// Корневой контекст процесса: отменяется по Ctrl+C или SIGTERM (docker stop, systemd).
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	startupCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	pool, err := postgres.Connect(startupCtx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	slog.Info("postgres connected")

	if err := postgres.Migrate(startupCtx, pool); err != nil {
		return err
	}
	slog.Info("migrations applied")

	svc := app.NewServices(cfg, pool, slog.Default())

	// Правила выбора серверов применяются сразу: конфиг мог измениться с прошлого скана.
	if st, err := svc.Scanner.ApplyTrackingRules(startupCtx); err != nil {
		slog.Warn("tracking rules at startup", "err", err)
	} else {
		slog.Info("tracking rules applied", "manual", st.Manual, "auto", st.Auto, "disabled", st.Disabled)
	}

	// Фоновые циклы. Каждый — горутина; первый вернувший ошибку отменяет ctx остальным.
	g, ctx := errgroup.WithContext(ctx)
	g.Go(func() error { return svc.Scanner.RunLoop(ctx, cfg.LobbyScanInterval, cfg.LobbyScanRetry) })
	g.Go(func() error { return svc.Tracker.RunLoop(ctx) })
	g.Go(func() error { return svc.Scanner.RunRetentionLoop(ctx, cfg.RetentionInterval) })

	srv := httpapi.NewServer(cfg.HTTPAddr, postgres.NewStatusRepo(pool))
	g.Go(func() error {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("http: %w", err)
		}
		return nil
	})
	g.Go(func() error {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	})

	slog.Info("observer started", "http_addr", cfg.HTTPAddr, "lobby_scan_interval", cfg.LobbyScanInterval.String(),
		"poll_interval", cfg.PollInterval.String(), "manual_tracked", len(cfg.TrackHostAddresses),
		"auto_min_players", cfg.TrackAutoMinPlayers, "auto_max_servers", cfg.TrackAutoMaxServers)

	err = g.Wait()
	if errors.Is(err, context.Canceled) {
		slog.Info("observer stopped")
		return nil
	}
	return err
}
