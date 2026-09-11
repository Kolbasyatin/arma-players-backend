package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"armaplayers/internal/bohemia"
	"armaplayers/internal/catalog"
	"armaplayers/internal/observation"
)

// CatalogRepo — реализация catalog.Repository на pgx.
type CatalogRepo struct {
	pool *pgxpool.Pool
}

func NewCatalogRepo(pool *pgxpool.Pool) *CatalogRepo { return &CatalogRepo{pool: pool} }

// Проверка на этапе компиляции, что тип реализует интерфейс.
var _ catalog.Repository = (*CatalogRepo)(nil)

const sourceLobbyScan = "LOBBY_SCAN"

// SaveLobbyPage — одна транзакция на страницу. Порядок: raw_payload → для каждой комнаты
// резолюция server по HOST_ADDRESS (или создание) → upsert ключей → снимок.
func (r *CatalogRepo) SaveLobbyPage(ctx context.Context, observedAt time.Time, rooms []bohemia.Room, raw []byte, rawExpiresAt time.Time) (catalog.PageStats, error) {
	var stats catalog.PageStats

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return stats, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx) // no-op после успешного Commit

	var rawID int64
	err = tx.QueryRow(ctx,
		`INSERT INTO raw_payload (kind, fetched_at, expires_at, payload) VALUES ('SEARCH_ROOMS', $1, $2, $3) RETURNING id`,
		observedAt, rawExpiresAt, raw).Scan(&rawID)
	if err != nil {
		return stats, fmt.Errorf("insert raw_payload: %w", err)
	}

	for i := range rooms {
		room := &rooms[i]
		if room.HostAddress == "" {
			continue // без адреса сервер не идентифицировать; такие комнаты пока пропускаем
		}

		serverID, modHash, created, err := resolveOrCreateServer(ctx, tx, room, observedAt)
		if err != nil {
			return stats, fmt.Errorf("room %s (%s): %w", room.ID, room.HostAddress, err)
		}
		if created {
			stats.ServersCreated++
		}

		if err := upsertIdentityKeys(ctx, tx, serverID, room, observedAt); err != nil {
			return stats, fmt.Errorf("room %s: identity keys: %w", room.ID, err)
		}
		if _, err := syncServerMods(ctx, tx, serverID, modHash, room.Mods, observedAt); err != nil {
			return stats, fmt.Errorf("room %s: mods: %w", room.ID, err)
		}
		if err := insertObservation(ctx, tx, serverID, room, observedAt, sourceLobbyScan, &rawID); err != nil {
			return stats, fmt.Errorf("room %s: observation: %w", room.ID, err)
		}
		stats.Rooms++
	}

	if err := tx.Commit(ctx); err != nil {
		return stats, fmt.Errorf("commit: %w", err)
	}
	return stats, nil
}

// resolveOrCreateServer ищет канонический сервер сначала по ROOM_ID, затем по HOST_ADDRESS; не найдя — создаёт.
// Порядок обоснован данными 2026-09-11: за сутки 129 серверов сменили порт/IP, сохранив roomId и sessionId,
// и лишь 18 сменили roomId при рестарте. Адрес переиспользуется хостингом, roomId (UUID) — нет.
// В обоих случаях обновляются «текущие» поля сервера, так что переезд адреса подхватывается автоматически.
func resolveOrCreateServer(ctx context.Context, tx pgx.Tx, room *bohemia.Room, observedAt time.Time) (serverID int64, modHash string, created bool, err error) {
	var hash *string
	err = tx.QueryRow(ctx, `
		SELECT c.id, c.mod_set_hash
		FROM server_identity_key k
		JOIN server s ON s.id = k.server_id
		JOIN server c ON c.id = COALESCE(s.merged_into_server_id, s.id)
		WHERE (k.key_type = 'ROOM_ID' AND k.key_value = $1)
		   OR (k.key_type = 'HOST_ADDRESS' AND k.key_value = $2)
		ORDER BY (k.key_type = 'ROOM_ID') DESC, k.last_seen_at DESC
		LIMIT 1`, room.ID, room.HostAddress).Scan(&serverID, &hash)
	if hash != nil {
		modHash = *hash
	}

	switch {
	case errors.Is(err, pgx.ErrNoRows):
		err = tx.QueryRow(ctx, `
			INSERT INTO server (display_name, current_host_address, current_room_id, first_seen_at, last_seen_at)
			VALUES ($1, $2, $3, $4, $4)
			RETURNING id`, room.Name, room.HostAddress, room.ID, observedAt).Scan(&serverID)
		if err != nil {
			return 0, "", false, fmt.Errorf("insert server: %w", err)
		}
		created = true
	case err != nil:
		return 0, "", false, fmt.Errorf("resolve server: %w", err)
	default:
		_, err = tx.Exec(ctx, `
			UPDATE server
			SET display_name = $2, current_host_address = $3, current_room_id = $4,
			    last_seen_at = $5, active = true, updated_at = now()
			WHERE id = $1`, serverID, room.Name, room.HostAddress, room.ID, observedAt)
		if err != nil {
			return 0, "", false, fmt.Errorf("update server: %w", err)
		}
	}
	return serverID, modHash, created, nil
}

func upsertIdentityKeys(ctx context.Context, tx pgx.Tx, serverID int64, room *bohemia.Room, observedAt time.Time) error {
	keys := [][2]string{
		{"HOST_ADDRESS", room.HostAddress},
		{"ROOM_ID", room.ID},
		{"SESSION_ID", room.SessionID},
	}
	batch := &pgx.Batch{}
	for _, k := range keys {
		if k[1] == "" {
			continue
		}
		batch.Queue(`
			INSERT INTO server_identity_key (server_id, key_type, key_value, first_seen_at, last_seen_at)
			VALUES ($1, $2, $3, $4, $4)
			ON CONFLICT (server_id, key_type, key_value)
			DO UPDATE SET last_seen_at = EXCLUDED.last_seen_at,
			              observation_count = server_identity_key.observation_count + 1`,
			serverID, k[0], k[1], observedAt)
	}
	return tx.SendBatch(ctx, batch).Close()
}

func insertObservation(ctx context.Context, tx pgx.Tx, serverID int64, room *bohemia.Room, observedAt time.Time, source string, rawID *int64) error {
	var queueSize, queueMax, queueWait *int
	var queueType *string
	if q := room.JoinQueue; q != nil {
		queueSize, queueMax, queueWait = &q.Size, &q.MaxSize, &q.PositionAvgWaitTime
		if q.Type != "" {
			queueType = &q.Type
		}
	}
	var hostedScenarioMod *string
	if room.HostedScenarioModID != "" {
		hostedScenarioMod = &room.HostedScenarioModID
	}
	var modHash *string
	if h := modSetHash(room.Mods); h != "" {
		modHash = &h
	}
	var clientTypes []string
	if len(room.SupportedGameClientTypes) > 0 {
		clientTypes = room.SupportedGameClientTypes
	}
	var fps *int
	var memory *int64
	if rs := room.RuntimeStats; rs != nil {
		fps = &rs.FPS
		m := int64(rs.Memory)
		memory = &m
	}

	_, err := tx.Exec(ctx, `
		INSERT INTO server_observation (
			server_id, observed_at, source, room_id, session_id, host_address,
			name, scenario_id, scenario_name, game_version, host_type, platform_name, ping_site_id,
			player_count, player_limit, queue_size, queue_max_size, queue_avg_wait_time,
			direct_join_code, battl_eye, official, joinable, visible, password_protected,
			runtime_fps, runtime_memory, data_updated_at, details_updated_at, raw_payload_id,
			mod_count, mod_set_hash, hosted_scenario_mod_id, supported_game_client_types, queue_type, flags, last_joined_at
		) VALUES (
			$1, $2, $3, $4, $5, $6,
			$7, $8, $9, $10, $11, $12, $13,
			$14, $15, $16, $17, $18,
			$19, $20, $21, $22, $23, $24,
			$25, $26, $27, $28, $29,
			$30, $31, $32, $33, $34, $35, $36
		)`,
		serverID, observedAt, source, room.ID, room.SessionID, room.HostAddress,
		room.Name, room.ScenarioID, room.ScenarioName, room.GameVersion, room.HostType, room.PlatformName, room.PingSiteID,
		room.PlayerCount, room.PlayerCountLimit, queueSize, queueMax, queueWait,
		room.DirectJoinCode, room.BattlEye, room.Official, room.Joinable, room.Visible, room.PasswordProtected,
		fps, memory, unixOrNil(room.Updated), unixOrNil(room.DetailsUpdatedAt), rawID,
		len(room.Mods), modHash, hostedScenarioMod, clientTypes, queueType, room.Flags, unixOrNil(room.LastJoinedAt),
	)
	return err
}

// unixOrNil переводит unix-секунды из API в timestamptz; 0 означает «поля не было».
func unixOrNil(sec int64) *time.Time {
	if sec == 0 {
		return nil
	}
	t := time.Unix(sec, 0).UTC()
	return &t
}

// DeactivateUnseen — серверы, активные на момент скана, но не встреченные с его начала, исчезли из лобби.
func (r *CatalogRepo) DeactivateUnseen(ctx context.Context, scanStartedAt time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		UPDATE server SET active = false, updated_at = now()
		WHERE active AND merged_into_server_id IS NULL AND last_seen_at < $1`, scanStartedAt)
	if err != nil {
		return 0, fmt.Errorf("deactivate unseen: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (r *CatalogRepo) RecordPollRun(ctx context.Context, run observation.PollRun) error {
	var httpStatus *int
	if run.HTTPStatus != 0 {
		httpStatus = &run.HTTPStatus
	}
	var errMsg *string
	if run.ErrorMessage != "" {
		errMsg = &run.ErrorMessage
	}
	var roomID *string
	if run.RoomID != "" {
		roomID = &run.RoomID
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO poll_run (server_id, poll_type, started_at, finished_at, status, room_id, http_status,
		                      error_message, connected_count, queue_count, data_updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`,
		run.ServerID, string(run.Type), run.StartedAt, run.FinishedAt, string(run.Status), roomID, httpStatus,
		errMsg, run.ConnectedCount, run.QueueCount, run.DataUpdatedAt)
	if err != nil {
		return fmt.Errorf("insert poll_run: %w", err)
	}
	return nil
}

func (r *CatalogRepo) LastSuccessfulScanAt(ctx context.Context) (time.Time, bool, error) {
	var at time.Time
	err := r.pool.QueryRow(ctx, `
		SELECT finished_at FROM poll_run
		WHERE poll_type = $1 AND status = $2
		ORDER BY finished_at DESC LIMIT 1`,
		string(observation.PollLobbyScan), string(observation.StatusSuccess)).Scan(&at)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return time.Time{}, false, nil
	case err != nil:
		return time.Time{}, false, fmt.Errorf("last scan: %w", err)
	}
	return at, true, nil
}

// ApplyTrackingRules — ADR 0008. Конфиг — источник истины: MANUAL по списку адресов,
// AUTO — top-N активных по онлайну последнего снимка, остальное выключается.
func (r *CatalogRepo) ApplyTrackingRules(ctx context.Context, rules catalog.TrackingRules) (catalog.TrackingStats, error) {
	var st catalog.TrackingStats
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return st, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	manual := rules.ManualHosts
	if manual == nil {
		manual = []string{}
	}

	// 1. MANUAL.
	tag, err := tx.Exec(ctx, `
		UPDATE server SET tracking_enabled = true, tracking_source = 'MANUAL', updated_at = now()
		WHERE merged_into_server_id IS NULL AND current_host_address = ANY($1)`, manual)
	if err != nil {
		return st, fmt.Errorf("manual: %w", err)
	}
	st.Manual = tag.RowsAffected()

	// 2. AUTO: по пиковому онлайну за окно lookback, а не по последнему снимку —
	// иначе рестарт сервера в момент скана выбросил бы его из выборки на сутки.
	autoMax := rules.AutoMaxServers
	if rules.AutoMinPlayers <= 0 {
		autoMax = 0
	}
	lookback := rules.AutoLookback
	if lookback <= 0 {
		lookback = 7 * 24 * time.Hour
	}
	tag, err = tx.Exec(ctx, `
		WITH peak AS (
			SELECT server_id, max(player_count) AS peak_players
			FROM server_observation
			WHERE observed_at > now() - $4::interval AND player_count IS NOT NULL
			GROUP BY server_id
		),
		chosen AS (
			SELECT s.id
			FROM server s JOIN peak p ON p.server_id = s.id
			WHERE s.active AND s.merged_into_server_id IS NULL
			  AND COALESCE(s.tracking_source, '') <> 'MANUAL'
			  AND NOT (s.current_host_address = ANY($1))
			  AND p.peak_players >= $2
			ORDER BY p.peak_players DESC, s.id
			LIMIT $3
		)
		UPDATE server SET tracking_enabled = true, tracking_source = 'AUTO', updated_at = now()
		WHERE id IN (SELECT id FROM chosen)`, manual, rules.AutoMinPlayers, autoMax, lookback)
	if err != nil {
		return st, fmt.Errorf("auto: %w", err)
	}
	st.Auto = tag.RowsAffected()

	// 3. Всё остальное с флагом — выключить: MANUAL вне списка и AUTO вне top-N.
	// Только что включённые помечены updated_at = now() в этой транзакции; их не трогаем.
	tag, err = tx.Exec(ctx, `
		UPDATE server SET tracking_enabled = false, tracking_source = NULL, updated_at = now()
		WHERE tracking_enabled AND updated_at < now()`)
	if err != nil {
		return st, fmt.Errorf("disable: %w", err)
	}
	st.Disabled = tag.RowsAffected()

	return st, tx.Commit(ctx)
}

func (r *CatalogRepo) DeleteExpiredRawPayloads(ctx context.Context, now time.Time) (int64, error) {
	tag, err := r.pool.Exec(ctx, `DELETE FROM raw_payload WHERE expires_at < $1`, now)
	if err != nil {
		return 0, fmt.Errorf("delete expired raw_payload: %w", err)
	}
	return tag.RowsAffected(), nil
}

// MergeDuplicateRooms схлопывает серверы, у которых один и тот же ROOM_ID оказался у нескольких
// канонических записей (переезд адреса между сканами до введения резолюции по roomId, гонки).
// Более новая запись мягко сливается в более старую (ADR 0004): история не переписывается,
// флаг tracking переносится, текущий адрес берётся у новой записи.
func (r *CatalogRepo) MergeDuplicateRooms(ctx context.Context) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		WITH dup AS (
			SELECT k.key_value AS room_id,
			       min(COALESCE(s.merged_into_server_id, s.id)) AS keep_id,
			       array_agg(DISTINCT COALESCE(s.merged_into_server_id, s.id)) AS ids
			FROM server_identity_key k JOIN server s ON s.id = k.server_id
			WHERE k.key_type = 'ROOM_ID'
			GROUP BY k.key_value
			HAVING count(DISTINCT COALESCE(s.merged_into_server_id, s.id)) > 1
		),
		victims AS (
			SELECT d.keep_id, v.id AS victim_id
			FROM dup d JOIN server v ON v.id = ANY(d.ids) AND v.id <> d.keep_id AND v.merged_into_server_id IS NULL
		),
		moved AS (
			-- канонической записи достаются актуальные поля и флаг отслеживания жертвы
			UPDATE server k SET
				current_host_address = COALESCE(v.current_host_address, k.current_host_address),
				current_room_id      = COALESCE(v.current_room_id, k.current_room_id),
				display_name         = v.display_name,
				last_seen_at         = GREATEST(k.last_seen_at, v.last_seen_at),
				active               = k.active OR v.active,
				tracking_enabled     = k.tracking_enabled OR v.tracking_enabled,
				tracking_source      = COALESCE(k.tracking_source, v.tracking_source),
				updated_at           = now()
			FROM victims x JOIN server v ON v.id = x.victim_id
			WHERE k.id = x.keep_id AND v.last_seen_at >= k.last_seen_at
			RETURNING k.id
		),
		merged AS (
			UPDATE server v SET merged_into_server_id = x.keep_id, tracking_enabled = false, tracking_source = NULL, active = false, updated_at = now()
			FROM victims x WHERE v.id = x.victim_id
			RETURNING v.id, x.keep_id
		)
		INSERT INTO server_merge (source_server_id, target_server_id, merged_at, merged_by, reason)
		SELECT id, keep_id, now(), 'auto', 'same ROOM_ID observed under different hostAddress' FROM merged`)
	if err != nil {
		return 0, fmt.Errorf("merge duplicate rooms: %w", err)
	}
	return tag.RowsAffected(), nil
}
