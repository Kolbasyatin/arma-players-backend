package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"armaplayers/internal/httpapi"
)

// StatusRepo — httpapi.StatusProvider: агрегаты для /observation-status.
type StatusRepo struct {
	pool *pgxpool.Pool
}

func NewStatusRepo(pool *pgxpool.Pool) *StatusRepo { return &StatusRepo{pool: pool} }

var _ httpapi.StatusProvider = (*StatusRepo)(nil)

func (r *StatusRepo) Status(ctx context.Context) (httpapi.Status, error) {
	st := httpapi.Status{Now: time.Now().UTC(), PollsLastHour: map[string]int{}}

	err := r.pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM server),
		  (SELECT count(*) FROM server WHERE active AND merged_into_server_id IS NULL),
		  (SELECT count(*) FROM server WHERE tracking_enabled),
		  (SELECT count(*) FROM player_identity),
		  (SELECT count(*) FROM player_server_session WHERE ended_at IS NULL),
		  (SELECT count(*) FROM player_queue_session WHERE ended_at IS NULL),
		  (SELECT count(*) FROM domain_event WHERE processed_at IS NULL)`).Scan(
		&st.ServersTotal, &st.ServersActive, &st.ServersTracked, &st.PlayersKnown, &st.SessionsOpen, &st.QueueSessionsOpen, &st.EventsUnprocessed)
	if err != nil {
		return st, fmt.Errorf("status counters: %w", err)
	}

	var lastScanAt *time.Time
	var lastScanStatus *string
	err = r.pool.QueryRow(ctx, `
		SELECT finished_at, status FROM poll_run WHERE poll_type = 'LOBBY_SCAN' ORDER BY finished_at DESC LIMIT 1`).Scan(&lastScanAt, &lastScanStatus)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return st, fmt.Errorf("last scan: %w", err)
	}
	st.LastLobbyScanAt = lastScanAt
	if lastScanStatus != nil {
		st.LastLobbyScanStatus = *lastScanStatus
	}

	rows, err := r.pool.Query(ctx, `
		SELECT status, count(*) FROM poll_run WHERE started_at > now() - interval '1 hour' GROUP BY status`)
	if err != nil {
		return st, fmt.Errorf("polls last hour: %w", err)
	}
	for rows.Next() {
		var status string
		var n int
		if err := rows.Scan(&status, &n); err != nil {
			rows.Close()
			return st, err
		}
		st.PollsLastHour[status] = n
	}
	rows.Close()

	rows, err = r.pool.Query(ctx, `
		SELECT s.id, s.display_name, s.current_host_address, COALESCE(s.tracking_source, ''),
		       lp.finished_at, lp.status,
		       ls.finished_at, ls.connected_count, ls.queue_count,
		       (SELECT count(*) FROM player_server_session ps WHERE ps.server_id = s.id AND ps.ended_at IS NULL)
		FROM server s
		LEFT JOIN LATERAL (
			SELECT finished_at, status FROM poll_run
			WHERE server_id = s.id AND poll_type = 'LIST_PLAYERS' ORDER BY finished_at DESC LIMIT 1) lp ON true
		LEFT JOIN LATERAL (
			SELECT finished_at, connected_count, queue_count FROM poll_run
			WHERE server_id = s.id AND poll_type = 'LIST_PLAYERS' AND status = 'SUCCESS' ORDER BY finished_at DESC LIMIT 1) ls ON true
		WHERE s.tracking_enabled
		ORDER BY s.display_name`)
	if err != nil {
		return st, fmt.Errorf("tracked servers: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var t httpapi.TrackedServer
		var host, lastStatus *string
		if err := rows.Scan(&t.ServerID, &t.Name, &host, &t.TrackingSource, &t.LastPollAt, &lastStatus, &t.LastSuccessAt, &t.Players, &t.Queue, &t.SessionsOpen); err != nil {
			return st, err
		}
		if host != nil {
			t.HostAddress = *host
		}
		if lastStatus != nil {
			t.LastPollStatus = *lastStatus
		}
		if t.LastSuccessAt != nil {
			age := int64(st.Now.Sub(*t.LastSuccessAt) / time.Second)
			t.DataAgeSeconds = &age
		}
		st.Tracked = append(st.Tracked, t)
	}
	return st, rows.Err()
}
