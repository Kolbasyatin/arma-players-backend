package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"armaplayers/internal/httpapi"
)

// APIRepo — httpapi.Store на pgx: только чтение.
type APIRepo struct {
	pool *pgxpool.Pool
}

func NewAPIRepo(pool *pgxpool.Pool) *APIRepo { return &APIRepo{pool: pool} }

var _ httpapi.Store = (*APIRepo)(nil)

// Events — лента по курсору. Ник и длительность берём из сессии события; для событий без сессии — текущий ник.
func (r *APIRepo) Events(ctx context.Context, q httpapi.EventsQuery) ([]httpapi.Event, error) {
	var playerIDs []int64
	if len(q.PlayerIDs) > 0 {
		playerIDs = q.PlayerIDs
	}
	var types []string
	if len(q.Types) > 0 {
		types = q.Types
	}
	rows, err := r.pool.Query(ctx, `
		SELECT e.id, e.event_type, e.occurred_at, e.player_id, p.bohemia_user_id::text,
		       COALESCE(ps.nickname, qs.nickname, p.current_nickname, ''),
		       e.server_id, COALESCE(s.display_name, ''), e.session_id,
		       CASE
		         WHEN e.event_type = 'PLAYER_LEFT_SERVER' AND ps.id IS NOT NULL THEN EXTRACT(EPOCH FROM COALESCE(ps.ended_at, ps.last_seen_at) - ps.first_seen_at)::bigint
		         WHEN e.event_type = 'PLAYER_LEFT_QUEUE'  AND qs.id IS NOT NULL THEN EXTRACT(EPOCH FROM COALESCE(qs.ended_at, qs.last_seen_at) - qs.first_seen_at)::bigint
		       END,
		       e.payload, e.startup_replay, e.after_data_gap
		FROM domain_event e
		JOIN player_identity p ON p.id = e.player_id
		LEFT JOIN server s ON s.id = e.server_id
		LEFT JOIN player_server_session ps ON ps.id = e.session_id AND e.event_type IN ('PLAYER_JOINED_SERVER','PLAYER_LEFT_SERVER')
		LEFT JOIN player_queue_session  qs ON qs.id = e.session_id AND e.event_type IN ('PLAYER_ENTERED_QUEUE','PLAYER_LEFT_QUEUE')
		WHERE e.id > $1
		  AND ($2::bigint[] IS NULL OR e.player_id = ANY($2))
		  AND ($3::text[]   IS NULL OR e.event_type = ANY($3))
		  AND ($4::boolean OR NOT e.startup_replay)
		ORDER BY e.id
		LIMIT $5`, q.After, playerIDs, types, q.IncludeReplay, q.Limit)
	if err != nil {
		return nil, fmt.Errorf("events: %w", err)
	}
	defer rows.Close()

	var out []httpapi.Event
	for rows.Next() {
		var e httpapi.Event
		var payload []byte
		if err := rows.Scan(&e.ID, &e.Type, &e.OccurredAt, &e.PlayerID, &e.BohemiaUserID, &e.Nickname,
			&e.ServerID, &e.ServerName, &e.SessionID, &e.DurationSeconds, &payload, &e.StartupReplay, &e.AfterDataGap); err != nil {
			return nil, err
		}
		e.Payload = json.RawMessage(payload)
		if len(e.Payload) == 0 {
			e.Payload = json.RawMessage(`{}`)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (r *APIRepo) EventsHead(ctx context.Context) (int64, error) {
	var head int64
	if err := r.pool.QueryRow(ctx, `SELECT COALESCE(max(id), 0) FROM domain_event`).Scan(&head); err != nil {
		return 0, fmt.Errorf("events head: %w", err)
	}
	return head, nil
}

const playerSummarySQL = `
	SELECT p.id, p.bohemia_user_id::text, p.current_nickname, p.first_seen_at, p.last_seen_at,
	       COALESCE((SELECT array_agg(a.nickname ORDER BY a.last_seen_at DESC) FROM player_alias a WHERE a.player_id = p.id), '{}'),
	       COALESCE((SELECT json_agg(json_build_object('type', pl.game_client_type, 'id', pl.platform_user_id, 'last_seen_at', pl.last_seen_at) ORDER BY pl.last_seen_at DESC)
	                 FROM player_platform_identity pl WHERE pl.player_id = p.id), '[]'),
	       (SELECT count(*) FROM player_server_session ss WHERE ss.player_id = p.id),
	       o.id, o.name, o.host, o.since,
	       l.id, l.name, l.host
	FROM player_identity p
	LEFT JOIN LATERAL (
		SELECT c.id, c.display_name AS name, COALESCE(c.current_host_address,'') AS host, ss.first_seen_at AS since
		FROM player_server_session ss JOIN server sv ON sv.id = ss.server_id JOIN server c ON c.id = COALESCE(sv.merged_into_server_id, sv.id)
		WHERE ss.player_id = p.id AND ss.ended_at IS NULL ORDER BY ss.last_seen_at DESC LIMIT 1) o ON true
	LEFT JOIN LATERAL (
		SELECT c.id, c.display_name AS name, COALESCE(c.current_host_address,'') AS host
		FROM player_server_session ss JOIN server sv ON sv.id = ss.server_id JOIN server c ON c.id = COALESCE(sv.merged_into_server_id, sv.id)
		WHERE ss.player_id = p.id ORDER BY ss.last_seen_at DESC LIMIT 1) l ON true`

func scanPlayer(row pgx.Row) (httpapi.PlayerSummary, error) {
	var p httpapi.PlayerSummary
	var platforms []byte
	var oID, lID *int64
	var oName, oHost, lName, lHost *string
	var oSince *time.Time
	if err := row.Scan(&p.PlayerID, &p.BohemiaUserID, &p.CurrentNickname, &p.FirstSeenAt, &p.LastSeenAt, &p.Aliases, &platforms, &p.SessionsTotal,
		&oID, &oName, &oHost, &oSince, &lID, &lName, &lHost); err != nil {
		return p, err
	}
	if err := json.Unmarshal(platforms, &p.Platforms); err != nil {
		return p, fmt.Errorf("platforms: %w", err)
	}
	if p.Platforms == nil {
		p.Platforms = []httpapi.PlatformID{}
	}
	if oID != nil {
		p.Online = &httpapi.ServerRef{ID: *oID, Name: *oName, HostAddress: *oHost, Since: oSince}
	}
	if lID != nil {
		p.LastServer = &httpapi.ServerRef{ID: *lID, Name: *lName, HostAddress: *lHost}
	}
	return p, nil
}

// SearchPlayers — по подстроке любого когда-либо наблюдённого ника, без учёта регистра.
func (r *APIRepo) SearchPlayers(ctx context.Context, nick string, limit int) ([]httpapi.PlayerSummary, error) {
	rows, err := r.pool.Query(ctx, playerSummarySQL+`
		WHERE p.id IN (SELECT a.player_id FROM player_alias a WHERE lower(a.nickname) LIKE '%' || lower($1) || '%')
		ORDER BY p.last_seen_at DESC
		LIMIT $2`, nick, limit)
	if err != nil {
		return nil, fmt.Errorf("search players: %w", err)
	}
	defer rows.Close()
	var out []httpapi.PlayerSummary
	for rows.Next() {
		p, err := scanPlayer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PlayersByIDs — карточки нескольких игроков одним запросом: статус всех подписок бота за один вызов.
func (r *APIRepo) PlayersByIDs(ctx context.Context, ids []int64) ([]httpapi.PlayerSummary, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := r.pool.Query(ctx, playerSummarySQL+`
		WHERE p.id = ANY($1)
		ORDER BY (o.id IS NULL), p.last_seen_at DESC`, ids)
	if err != nil {
		return nil, fmt.Errorf("players by ids: %w", err)
	}
	defer rows.Close()
	var out []httpapi.PlayerSummary
	for rows.Next() {
		p, err := scanPlayer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *APIRepo) Player(ctx context.Context, id int64) (httpapi.PlayerSummary, bool, error) {
	p, err := scanPlayer(r.pool.QueryRow(ctx, playerSummarySQL+` WHERE p.id = $1`, id))
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return httpapi.PlayerSummary{}, false, nil
	case err != nil:
		return httpapi.PlayerSummary{}, false, fmt.Errorf("player %d: %w", id, err)
	}
	return p, true, nil
}

func (r *APIRepo) PlayerSessions(ctx context.Context, id int64, limit int) ([]httpapi.Session, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT ss.id, c.id, c.display_name, ss.nickname, ss.first_seen_at, ss.last_seen_at, ss.ended_at, ss.status,
		       EXTRACT(EPOCH FROM COALESCE(ss.ended_at, ss.last_seen_at) - ss.first_seen_at)::bigint
		FROM player_server_session ss
		JOIN server sv ON sv.id = ss.server_id
		JOIN server c ON c.id = COALESCE(sv.merged_into_server_id, sv.id)
		WHERE ss.player_id = $1
		ORDER BY ss.first_seen_at DESC
		LIMIT $2`, id, limit)
	if err != nil {
		return nil, fmt.Errorf("player sessions: %w", err)
	}
	defer rows.Close()
	var out []httpapi.Session
	for rows.Next() {
		var s httpapi.Session
		if err := rows.Scan(&s.ID, &s.ServerID, &s.ServerName, &s.Nickname, &s.FirstSeenAt, &s.LastSeenAt, &s.EndedAt, &s.Status, &s.DurationSeconds); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (r *APIRepo) Servers(ctx context.Context, trackedOnly bool, nameFilter string) ([]httpapi.ServerSummary, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT s.id, s.display_name, COALESCE(s.current_host_address, ''), s.active, s.tracking_enabled, COALESCE(s.tracking_source, ''),
		       o.player_count, o.player_limit, o.queue_size, o.observed_at, s.last_seen_at
		FROM server s
		LEFT JOIN LATERAL (SELECT player_count, player_limit, queue_size, observed_at FROM server_observation WHERE server_id = s.id ORDER BY observed_at DESC LIMIT 1) o ON true
		WHERE s.merged_into_server_id IS NULL
		  AND (NOT $1::boolean OR s.tracking_enabled)
		  AND ($2 = '' OR s.display_name ILIKE '%' || $2 || '%')
		ORDER BY s.tracking_enabled DESC, o.player_count DESC NULLS LAST, s.display_name
		LIMIT 500`, trackedOnly, nameFilter)
	if err != nil {
		return nil, fmt.Errorf("servers: %w", err)
	}
	defer rows.Close()
	var out []httpapi.ServerSummary
	for rows.Next() {
		var s httpapi.ServerSummary
		if err := rows.Scan(&s.ID, &s.Name, &s.HostAddress, &s.Active, &s.Tracked, &s.TrackingSource, &s.Players, &s.PlayerLimit, &s.Queue, &s.ObservedAt, &s.LastSeenAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}
