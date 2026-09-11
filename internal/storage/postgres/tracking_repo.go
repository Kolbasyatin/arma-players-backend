package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"armaplayers/internal/bohemia"
	"armaplayers/internal/observation"
	"armaplayers/internal/tracking"
)

// TrackingRepo — tracking.Repository на pgx. Переиспользует SQL каталога для снимков и ключей.
type TrackingRepo struct {
	pool *pgxpool.Pool
}

func NewTrackingRepo(pool *pgxpool.Pool) *TrackingRepo { return &TrackingRepo{pool: pool} }

var _ tracking.Repository = (*TrackingRepo)(nil)

const sourceTrackingPoll = "TRACKING_POLL"

func (r *TrackingRepo) TrackedServers(ctx context.Context) ([]tracking.Server, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT id, display_name, current_host_address, COALESCE(current_room_id, '')
		FROM server
		WHERE tracking_enabled AND merged_into_server_id IS NULL AND current_host_address IS NOT NULL
		ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("tracked servers: %w", err)
	}
	defer rows.Close()

	var out []tracking.Server
	for rows.Next() {
		var s tracking.Server
		if err := rows.Scan(&s.ID, &s.Name, &s.HostAddress, &s.CurrentRoomID); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *TrackingRepo) SaveRoomObservation(ctx context.Context, serverID int64, room bohemia.Room, observedAt time.Time, raw []byte, rawExpiresAt time.Time) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	var rawID int64
	if err := tx.QueryRow(ctx,
		`INSERT INTO raw_payload (kind, fetched_at, expires_at, payload) VALUES ('SEARCH_ROOMS', $1, $2, $3) RETURNING id`,
		observedAt, rawExpiresAt, raw).Scan(&rawID); err != nil {
		return fmt.Errorf("insert raw_payload: %w", err)
	}
	var currentHash *string
	if err := tx.QueryRow(ctx, `
		UPDATE server
		SET display_name = $2, current_room_id = $3, last_seen_at = $4, active = true, updated_at = now()
		WHERE id = $1
		RETURNING mod_set_hash`, serverID, room.Name, room.ID, observedAt).Scan(&currentHash); err != nil {
		return fmt.Errorf("update server: %w", err)
	}
	hash := ""
	if currentHash != nil {
		hash = *currentHash
	}
	if _, err := syncServerMods(ctx, tx, serverID, hash, room.Mods, observedAt); err != nil {
		return fmt.Errorf("mods: %w", err)
	}
	if err := upsertIdentityKeys(ctx, tx, serverID, &room, observedAt); err != nil {
		return fmt.Errorf("identity keys: %w", err)
	}
	if err := insertObservation(ctx, tx, serverID, &room, observedAt, sourceTrackingPoll, &rawID); err != nil {
		return fmt.Errorf("observation: %w", err)
	}
	return tx.Commit(ctx)
}

func (r *TrackingRepo) SaveRawPayload(ctx context.Context, kind string, fetchedAt, expiresAt time.Time, raw []byte) (int64, error) {
	var id int64
	err := r.pool.QueryRow(ctx,
		`INSERT INTO raw_payload (kind, fetched_at, expires_at, payload) VALUES ($1, $2, $3, $4) RETURNING id`,
		kind, fetchedAt, expiresAt, raw).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert raw_payload: %w", err)
	}
	return id, nil
}

// RecordPollRun — та же реализация, что у каталога.
func (r *TrackingRepo) RecordPollRun(ctx context.Context, run observation.PollRun) error {
	return (&CatalogRepo{pool: r.pool}).RecordPollRun(ctx, run)
}
