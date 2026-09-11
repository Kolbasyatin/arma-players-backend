// Package httpapi — служебный HTTP observer'а: /health и /observation-status (AGENTS §11, §24).
// Показывает свежесть данных, а не «0 игроков»: если poll'ы падают, это видно здесь.
package httpapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"time"
)

// Status — снимок здоровья наблюдения. Реализация StatusProvider — storage/postgres.StatusRepo.
type Status struct {
	Now                 time.Time       `json:"now"`
	LastLobbyScanAt     *time.Time      `json:"last_lobby_scan_at"`
	LastLobbyScanStatus string          `json:"last_lobby_scan_status,omitempty"`
	ServersTotal        int64           `json:"servers_total"`
	ServersActive       int64           `json:"servers_active"`
	ServersTracked      int64           `json:"servers_tracked"`
	PlayersKnown        int64           `json:"players_known"`
	SessionsOpen        int64           `json:"sessions_open"`
	QueueSessionsOpen   int64           `json:"queue_sessions_open"`
	EventsUnprocessed   int64           `json:"events_unprocessed"`
	PollsLastHour       map[string]int  `json:"polls_last_hour"` // status → count
	Tracked             []TrackedServer `json:"tracked"`
}

// TrackedServer — состояние одного отслеживаемого сервера.
type TrackedServer struct {
	ServerID       int64      `json:"server_id"`
	Name           string     `json:"name"`
	HostAddress    string     `json:"host_address"`
	TrackingSource string     `json:"tracking_source"`
	LastPollAt     *time.Time `json:"last_poll_at"`
	LastPollStatus string     `json:"last_poll_status,omitempty"`
	LastSuccessAt  *time.Time `json:"last_success_at"`
	DataAgeSeconds *int64     `json:"data_age_seconds"` // now − last_success_at; NULL если успехов не было
	Players        *int       `json:"players"`          // из последнего успешного poll
	Queue          *int       `json:"queue"`
	SessionsOpen   int64      `json:"sessions_open"`
}

type StatusProvider interface {
	Status(ctx context.Context) (Status, error)
}

func logger() *slog.Logger { return slog.Default() }

// NewServer собирает http.Server с таймаутами: сервис без них уязвим к зависшим клиентам.
// store и apiToken включают REST API для внешних потребителей (nil/"" — только служебные маршруты).
func NewServer(addr string, status StatusProvider, store Store, apiToken string) *http.Server {
	mux := http.NewServeMux()
	if store != nil {
		registerAPI(mux, store, apiToken)
	}
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /observation-status", func(w http.ResponseWriter, r *http.Request) {
		st, err := status.Status(r.Context())
		if err != nil {
			slog.Error("observation-status", "err", err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "status unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, st)
	})
	return &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
