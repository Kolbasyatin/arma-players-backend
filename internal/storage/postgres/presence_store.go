package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"armaplayers/internal/bohemia"
	"armaplayers/internal/observation"
	"armaplayers/internal/presence"
)

// PresenceStore — presence.Store на pgx: транзакция на одно наблюдение.
type PresenceStore struct {
	pool *pgxpool.Pool
}

func NewPresenceStore(pool *pgxpool.Pool) *PresenceStore { return &PresenceStore{pool: pool} }

var _ presence.Store = (*PresenceStore)(nil)

func (s *PresenceStore) WithTx(ctx context.Context, fn func(tx presence.Tx) error) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)

	if err := fn(&presenceTx{tx: tx}); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// presenceTx — presence.Tx поверх pgx.Tx. Неэкспортирован: создаётся только внутри WithTx.
type presenceTx struct {
	tx pgx.Tx
}

// UpsertPlayer — идентичность, платформенный маппинг и алиас одним заходом.
// Возвращает предыдущий ник, чтобы presence мог породить PLAYER_NICKNAME_CHANGED.
// touchInterval — как часто разрешено обновлять «служебные» поля игрока: время последнего
// появления и счётчики наблюдений в player_identity, player_alias, player_platform_identity.
//
// ЗАЧЕМ ЭТО ВООБЩЕ ЕСТЬ. Строк в этих таблицах ровно столько, сколько нужно: одна на игрока,
// одна на его ник, одна на платформенный аккаунт. Но раньше каждое наблюдение переписывало их
// заново, и на 150 тысячах строк накопилось 45 миллионов обновлений на таблицу. Postgres
// при обновлении пишет НОВУЮ версию строки, а старую помечает мёртвой, поэтому строка
// в 100 байт распухла до 4 килобайт, а таблицы до гигабайта каждая.
//
// Точность при этом не теряется: «когда игрока видели в последний раз» с точностью до опроса
// лежит в player_server_session, которая обновляется каждый poll по своей прямой надобности,
// и API берёт время оттуда. Здесь же хранится грубая отметка — с точностью до этого интервала.
const touchInterval = time.Hour

// UpsertPlayer заводит игрока, его ник и платформенный аккаунт, если их ещё нет.
//
// Существующие строки трогаются ТОЛЬКО когда что-то изменилось по сути (сменился текущий ник)
// или когда отметка времени устарела больше чем на touchInterval. Условие стоит в самом
// ON CONFLICT: если оно ложно, Postgres обновление не выполняет вовсе, а не пишет ту же строку
// заново — это и есть разница между семью миллионами обновлений в сутки и двумястами тысячами.
func (t *presenceTx) UpsertPlayer(ctx context.Context, p bohemia.Player, seenAt time.Time) (presence.PlayerRef, error) {
	var ref presence.PlayerRef

	// Состояние ДО записи. Именно player_identity.current_nickname решает, была ли смена ника:
	// это ровно тот ник, под которым мы видели игрока в прошлый раз.
	//
	// Брать предыдущий ник из player_alias (самый свежий по last_seen_at) нельзя: отметки времени
	// в алиасах обновляются не чаще touchInterval, поэтому «самым свежим» там ещё час числится
	// ник, который игрок уже сменил. Игрок, вернувший прежний ник (A → B → A), получал из-за
	// этого событие «B → A» на КАЖДОМ опросе следующего часа.
	existed := true
	err := t.tx.QueryRow(ctx,
		`SELECT id, current_nickname FROM player_identity WHERE bohemia_user_id = $1`, p.UserID).
		Scan(&ref.PlayerID, &ref.PreviousNickname)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		existed, ref.PreviousNickname = false, ""
	case err != nil:
		return ref, fmt.Errorf("player_identity lookup: %w", err)
	}
	ref.IsNew = !existed

	var id int64
	err = t.tx.QueryRow(ctx, `
		INSERT INTO player_identity (bohemia_user_id, current_nickname, first_seen_at, last_seen_at)
		VALUES ($1, $2, $3, $3)
		ON CONFLICT (bohemia_user_id) DO UPDATE
		   SET current_nickname = EXCLUDED.current_nickname,
		       last_seen_at     = GREATEST(player_identity.last_seen_at, EXCLUDED.last_seen_at),
		       updated_at       = now()
		   WHERE player_identity.current_nickname <> EXCLUDED.current_nickname
		      OR player_identity.last_seen_at < EXCLUDED.last_seen_at - $4::interval
		RETURNING id`,
		p.UserID, p.Username, seenAt, touchInterval).Scan(&id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// Условие DO UPDATE не выполнилось — строка есть и трогать её незачем, id уже прочитан выше.
		// Если же строки до нас не было, значит её вставил параллельный опрос между нашим SELECT
		// и INSERT (один игрок может попасть в опрос двух серверов: играет на одном, стоит
		// в очереди на другой). Тогда id читаем заново.
		if !existed {
			if err := t.tx.QueryRow(ctx,
				`SELECT id FROM player_identity WHERE bohemia_user_id = $1`, p.UserID).Scan(&ref.PlayerID); err != nil {
				return ref, fmt.Errorf("player_identity id: %w", err)
			}
		}
	case err != nil:
		return ref, fmt.Errorf("upsert player_identity: %w", err)
	default:
		ref.PlayerID = id
	}

	batch := &pgx.Batch{}
	// observation_count теперь считает не опросы, а интервалы присутствия: раз в touchInterval.
	// Как метрика «насколько часто игрок ходит под этим ником» она от этого не испортилась,
	// а обновлений стало на два порядка меньше.
	batch.Queue(`
		INSERT INTO player_alias (player_id, nickname, first_seen_at, last_seen_at)
		VALUES ($1, $2, $3, $3)
		ON CONFLICT (player_id, nickname) DO UPDATE
		   SET last_seen_at = GREATEST(player_alias.last_seen_at, EXCLUDED.last_seen_at),
		       observation_count = player_alias.observation_count + 1
		   WHERE player_alias.last_seen_at < EXCLUDED.last_seen_at - $4::interval`,
		ref.PlayerID, p.Username, seenAt, touchInterval)
	if p.PlatformUserID != "" {
		batch.Queue(`
			INSERT INTO player_platform_identity (player_id, game_client_type, platform_user_id, first_seen_at, last_seen_at)
			VALUES ($1, $2, $3, $4, $4)
			ON CONFLICT (player_id, game_client_type, platform_user_id) DO UPDATE
			   SET last_seen_at = GREATEST(player_platform_identity.last_seen_at, EXCLUDED.last_seen_at),
			       observation_count = player_platform_identity.observation_count + 1
			   WHERE player_platform_identity.last_seen_at < EXCLUDED.last_seen_at - $5::interval`,
			ref.PlayerID, p.GameClientType, p.PlatformUserID, seenAt, touchInterval)
	}
	if err := t.tx.SendBatch(ctx, batch).Close(); err != nil {
		return ref, fmt.Errorf("alias/platform upsert: %w", err)
	}
	return ref, nil
}

func (t *presenceTx) LastSuccessfulListPlayersAt(ctx context.Context, serverID int64) (time.Time, bool, error) {
	var at time.Time
	err := t.tx.QueryRow(ctx, `
		SELECT finished_at FROM poll_run
		WHERE server_id = $1 AND poll_type = $2 AND status = $3
		ORDER BY finished_at DESC LIMIT 1`,
		serverID, string(observation.PollListPlayers), string(observation.StatusSuccess)).Scan(&at)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return time.Time{}, false, nil
	case err != nil:
		return time.Time{}, false, fmt.Errorf("last list_players poll: %w", err)
	}
	return at, true, nil
}

const sessionColumns = `id, player_id, server_id, first_seen_at, last_seen_at, first_known_absent_at, ended_at, status, absent_polls, nickname`

func scanSessions(rows pgx.Rows, withResult bool) ([]presence.Session, error) {
	defer rows.Close()
	var out []presence.Session
	for rows.Next() {
		var s presence.Session
		var status string
		var result *string
		dest := []any{&s.ID, &s.PlayerID, &s.ServerID, &s.FirstSeenAt, &s.LastSeenAt, &s.FirstKnownAbsentAt, &s.EndedAt, &status, &s.AbsentPolls, &s.Nickname}
		if withResult {
			dest = append(dest, &result)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		s.Status = presence.SessionStatus(status)
		if result != nil {
			s.QueueResult = presence.QueueResult(*result)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (t *presenceTx) OpenPresenceSessions(ctx context.Context, serverID int64) ([]presence.Session, error) {
	rows, err := t.tx.Query(ctx, `SELECT `+sessionColumns+` FROM player_server_session WHERE server_id = $1 AND ended_at IS NULL FOR UPDATE`, serverID)
	if err != nil {
		return nil, fmt.Errorf("open presence sessions: %w", err)
	}
	return scanSessions(rows, false)
}

func (t *presenceTx) InsertPresenceSession(ctx context.Context, s presence.Session) (int64, error) {
	var id int64
	err := t.tx.QueryRow(ctx, `
		INSERT INTO player_server_session (player_id, server_id, first_seen_at, last_seen_at, status, startup_replay, nickname)
		VALUES ($1, $2, $3, $4, $5, $6, $7) RETURNING id`,
		s.PlayerID, s.ServerID, s.FirstSeenAt, s.LastSeenAt, string(s.Status), s.StartupReplay, s.Nickname).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert presence session: %w", err)
	}
	return id, nil
}

func (t *presenceTx) UpdatePresenceSession(ctx context.Context, s presence.Session) error {
	_, err := t.tx.Exec(ctx, `
		UPDATE player_server_session
		SET last_seen_at = $2, first_known_absent_at = $3, ended_at = $4, status = $5, absent_polls = $6, nickname = $7, updated_at = now()
		WHERE id = $1`, s.ID, s.LastSeenAt, s.FirstKnownAbsentAt, s.EndedAt, string(s.Status), s.AbsentPolls, s.Nickname)
	if err != nil {
		return fmt.Errorf("update presence session %d: %w", s.ID, err)
	}
	return nil
}

func (t *presenceTx) OpenQueueSessions(ctx context.Context, serverID int64) ([]presence.Session, error) {
	rows, err := t.tx.Query(ctx, `SELECT `+sessionColumns+`, result FROM player_queue_session WHERE server_id = $1 AND ended_at IS NULL FOR UPDATE`, serverID)
	if err != nil {
		return nil, fmt.Errorf("open queue sessions: %w", err)
	}
	return scanSessions(rows, true)
}

func (t *presenceTx) InsertQueueSession(ctx context.Context, s presence.Session) (int64, error) {
	var id int64
	err := t.tx.QueryRow(ctx, `
		INSERT INTO player_queue_session (player_id, server_id, first_seen_at, last_seen_at, status, nickname)
		VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`,
		s.PlayerID, s.ServerID, s.FirstSeenAt, s.LastSeenAt, string(s.Status), s.Nickname).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert queue session: %w", err)
	}
	return id, nil
}

func (t *presenceTx) UpdateQueueSession(ctx context.Context, s presence.Session) error {
	var result *string
	if s.QueueResult != "" {
		r := string(s.QueueResult)
		result = &r
	}
	_, err := t.tx.Exec(ctx, `
		UPDATE player_queue_session
		SET last_seen_at = $2, first_known_absent_at = $3, ended_at = $4, status = $5, absent_polls = $6, result = $7, nickname = $8, updated_at = now()
		WHERE id = $1`, s.ID, s.LastSeenAt, s.FirstKnownAbsentAt, s.EndedAt, string(s.Status), s.AbsentPolls, result, s.Nickname)
	if err != nil {
		return fmt.Errorf("update queue session %d: %w", s.ID, err)
	}
	return nil
}

func (t *presenceTx) AppendEvent(ctx context.Context, e presence.Event) error {
	payload := []byte(`{}`)
	if e.Payload != nil {
		var err error
		if payload, err = json.Marshal(e.Payload); err != nil {
			return fmt.Errorf("event payload: %w", err)
		}
	}
	var playerID, serverID *int64
	if e.PlayerID != 0 {
		playerID = &e.PlayerID
	}
	if e.ServerID != 0 {
		serverID = &e.ServerID
	}
	_, err := t.tx.Exec(ctx, `
		INSERT INTO domain_event (event_type, occurred_at, player_id, server_id, session_id, payload, startup_replay, after_data_gap)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		string(e.Type), e.OccurredAt, playerID, serverID, e.SessionID, payload, e.StartupReplay, e.AfterDataGap)
	if err != nil {
		return fmt.Errorf("insert domain_event: %w", err)
	}
	return nil
}
