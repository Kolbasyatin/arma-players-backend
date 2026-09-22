package postgres

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"armaplayers/internal/steam"
)

// SteamRepo — схема steam: список наблюдаемых, снимки, сводное состояние, граф друзей.
type SteamRepo struct {
	pool *pgxpool.Pool
}

func NewSteamRepo(pool *pgxpool.Pool) *SteamRepo { return &SteamRepo{pool: pool} }

// SaveSnapshot сохраняет сырьё источника, ЕСЛИ оно отличается от предыдущего.
// Возвращает true, если строка записана.
//
// Проверка обязательна и делается по источникам отдельно: playtime_2weeks у активного
// игрока меняется ежедневно, и без неё меняющиеся игры тянули бы за собой перезапись
// профиля, друзей и банов. Ровно так raw_payload вырос до 5,7 ГБ.
func (r *SteamRepo) SaveSnapshot(ctx context.Context, steamID, source string, fetchedAt time.Time, body []byte) (bool, error) {
	hash := sha256.Sum256(body)

	var lastHash []byte
	err := r.pool.QueryRow(ctx, `
		SELECT body_hash FROM steam.snapshot
		WHERE steam_id = $1 AND source = $2
		ORDER BY fetched_at DESC, id DESC LIMIT 1`, steamID, source).Scan(&lastHash)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
	case err != nil:
		return false, fmt.Errorf("steam: last snapshot: %w", err)
	default:
		if string(lastHash) == string(hash[:]) {
			return false, nil
		}
	}

	if _, err := r.pool.Exec(ctx, `
		INSERT INTO steam.snapshot (steam_id, source, fetched_at, body, body_hash)
		VALUES ($1, $2, $3, $4, $5)`, steamID, source, fetchedAt, body, hash[:]); err != nil {
		return false, fmt.Errorf("steam: save snapshot: %w", err)
	}

	return true, nil
}

// SaveProfile обновляет сводное состояние. COALESCE на каждом поле-указателе: источник
// мог не ответить, и затирать известное значение неизвестностью нельзя — иначе один
// сбой у Valve обнулял бы накопленное.
func (r *SteamRepo) SaveProfile(ctx context.Context, p steam.Profile) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO steam.profile (
			steam_id, persona_name, real_name, avatar_hash, profile_url, country_code,
			primary_clan_id, visibility, account_created_at,
			vac_banned, vac_ban_count, game_ban_count, days_since_last_ban, economy_ban,
			reforger_minutes, reforger_minutes_2w, games_visible, friends_visible, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18, now())
		ON CONFLICT (steam_id) DO UPDATE SET
			persona_name        = COALESCE(NULLIF(EXCLUDED.persona_name, ''), steam.profile.persona_name),
			real_name           = COALESCE(NULLIF(EXCLUDED.real_name, ''), steam.profile.real_name),
			avatar_hash         = COALESCE(NULLIF(EXCLUDED.avatar_hash, ''), steam.profile.avatar_hash),
			profile_url         = COALESCE(NULLIF(EXCLUDED.profile_url, ''), steam.profile.profile_url),
			country_code        = COALESCE(NULLIF(EXCLUDED.country_code, ''), steam.profile.country_code),
			primary_clan_id     = COALESCE(NULLIF(EXCLUDED.primary_clan_id, ''), steam.profile.primary_clan_id),
			visibility          = COALESCE(EXCLUDED.visibility, steam.profile.visibility),
			account_created_at  = COALESCE(EXCLUDED.account_created_at, steam.profile.account_created_at),
			vac_banned          = COALESCE(EXCLUDED.vac_banned, steam.profile.vac_banned),
			vac_ban_count       = COALESCE(EXCLUDED.vac_ban_count, steam.profile.vac_ban_count),
			game_ban_count      = COALESCE(EXCLUDED.game_ban_count, steam.profile.game_ban_count),
			days_since_last_ban = COALESCE(EXCLUDED.days_since_last_ban, steam.profile.days_since_last_ban),
			economy_ban         = COALESCE(NULLIF(EXCLUDED.economy_ban, ''), steam.profile.economy_ban),
			reforger_minutes    = COALESCE(EXCLUDED.reforger_minutes, steam.profile.reforger_minutes),
			reforger_minutes_2w = COALESCE(EXCLUDED.reforger_minutes_2w, steam.profile.reforger_minutes_2w),
			games_visible       = EXCLUDED.games_visible,
			friends_visible     = EXCLUDED.friends_visible,
			updated_at          = now()`,
		p.SteamID, p.PersonaName, p.RealName, p.AvatarHash, p.ProfileURL, p.CountryCode,
		p.PrimaryClanID, p.Visibility, p.AccountCreatedAt,
		p.VACBanned, p.VACBanCount, p.GameBanCount, p.DaysSinceLastBan, p.EconomyBan,
		p.ReforgerMinutes, p.ReforgerMinutes2W, p.GamesVisible, p.FriendsVisible)
	if err != nil {
		return fmt.Errorf("steam: save profile: %w", err)
	}
	return nil
}

// SaveFriends обновляет граф. Пропавшие связи не удаляются, а помечаются lost_at:
// расфрендились — это такой же факт, как подружились, и терять его незачем.
//
// Вызывать ТОЛЬКО когда список действительно виден: на закрытом профиле пустой список
// пометил бы весь граф игрока потерянным.
func (r *SteamRepo) SaveFriends(ctx context.Context, steamID string, friends []steam.Friend, seenAt time.Time) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("steam: begin: %w", err)
	}
	defer tx.Rollback(ctx)

	ids := make([]string, 0, len(friends))
	batch := &pgx.Batch{}
	for _, friend := range friends {
		ids = append(ids, friend.SteamID)
		batch.Queue(`
			INSERT INTO steam.friend_edge (steam_id, friend_steam_id, friend_since, first_seen_at, last_seen_at)
			VALUES ($1, $2, $3, $4, $4)
			ON CONFLICT (steam_id, friend_steam_id) DO UPDATE
			   SET last_seen_at = GREATEST(steam.friend_edge.last_seen_at, EXCLUDED.last_seen_at),
			       friend_since = COALESCE(EXCLUDED.friend_since, steam.friend_edge.friend_since),
			       lost_at      = NULL`,
			steamID, friend.SteamID, friend.Since, seenAt)
	}
	if err := tx.SendBatch(ctx, batch).Close(); err != nil {
		return fmt.Errorf("steam: upsert friends: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE steam.friend_edge SET lost_at = $2
		WHERE steam_id = $1 AND lost_at IS NULL AND friend_steam_id <> ALL($3)`,
		steamID, seenAt, ids); err != nil {
		return fmt.Errorf("steam: mark lost friends: %w", err)
	}

	return tx.Commit(ctx)
}

// Watchlist — кого пора обогащать. Срок годности данных у каждого свой: активные игроки
// обновляются часто, давно не заходившие — редко.
//
// «Активен» определяется по НАШИМ наблюдениям, а не по данным Steam: связь steam_id → игрок
// идёт через player_platform_identity, а факт появления на серверах — через сессии.
// Именно поэтому схема steam лежит в той же базе: иначе этот запрос пришлось бы склеивать в коде.
func (r *SteamRepo) Watchlist(ctx context.Context, policy steam.WatchlistPolicy, now time.Time, limit int) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT w.steam_id
		FROM steam.watchlist w
		LEFT JOIN LATERAL (
			SELECT max(ss.last_seen_at) AS last_seen
			FROM player_platform_identity pl
			JOIN player_server_session ss ON ss.player_id = pl.player_id
			WHERE pl.game_client_type = 'PLATFORM_PC' AND pl.platform_user_id = w.steam_id
		) seen ON true
		WHERE w.enabled
		  AND (w.last_try_at IS NULL
		       OR w.last_try_at < $1::timestamptz - CASE
		            WHEN seen.last_seen >= $1::timestamptz - $2::interval THEN $3::interval
		            ELSE $4::interval
		          END)
		ORDER BY w.last_try_at NULLS FIRST
		LIMIT $5`,
		now, policy.ActiveWindow, policy.ActiveStaleness, policy.IdleStaleness, limit)
	if err != nil {
		return nil, fmt.Errorf("steam: watchlist: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// AddToWatchlist ставит игрока в очередь на обогащение. Повторное добавление не сбрасывает
// историю попыток — только обновляет заметку и включает обратно.
func (r *SteamRepo) AddToWatchlist(ctx context.Context, steamID, addedBy, note string) error {
	_, err := r.pool.Exec(ctx, `
		INSERT INTO steam.watchlist (steam_id, added_by, note)
		VALUES ($1, $2, $3)
		ON CONFLICT (steam_id) DO UPDATE
		   SET note = COALESCE(NULLIF(EXCLUDED.note, ''), steam.watchlist.note), enabled = true`,
		steamID, addedBy, note)
	if err != nil {
		return fmt.Errorf("steam: add to watchlist: %w", err)
	}
	return nil
}

// MarkTry отмечает попытку сбора. Пустой errText означает успех.
func (r *SteamRepo) MarkTry(ctx context.Context, steamID string, at time.Time, errText string) error {
	_, err := r.pool.Exec(ctx, `
		UPDATE steam.watchlist
		   SET last_try_at = $2,
		       last_ok_at  = CASE WHEN $3 = '' THEN $2 ELSE last_ok_at END,
		       last_error  = $3
		 WHERE steam_id = $1`, steamID, at, errText)
	if err != nil {
		return fmt.Errorf("steam: mark try: %w", err)
	}
	return nil
}

// SteamIDsOfPlayer — Steam-аккаунты нашего игрока. Обычно один, но таблица допускает несколько:
// уникальность там только внутри игрока, и смена аккаунта — наблюдаемый факт, а не ошибка.
func (r *SteamRepo) SteamIDsOfPlayer(ctx context.Context, playerID int64) ([]string, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT platform_user_id FROM player_platform_identity
		WHERE player_id = $1 AND game_client_type = 'PLATFORM_PC'
		ORDER BY last_seen_at DESC`, playerID)
	if err != nil {
		return nil, fmt.Errorf("steam: player steam ids: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Profile — сохранённое состояние. found=false, если игрока ещё ни разу не собирали.
func (r *SteamRepo) Profile(ctx context.Context, steamID string) (steam.StoredProfile, bool, error) {
	var p steam.StoredProfile
	err := r.pool.QueryRow(ctx, `
		SELECT steam_id, persona_name, real_name, avatar_hash, profile_url, country_code,
		       primary_clan_id, visibility, account_created_at,
		       vac_banned, vac_ban_count, game_ban_count, days_since_last_ban, economy_ban,
		       reforger_minutes, reforger_minutes_2w, games_visible, friends_visible, updated_at
		FROM steam.profile WHERE steam_id = $1`, steamID).
		Scan(&p.SteamID, &p.PersonaName, &p.RealName, &p.AvatarHash, &p.ProfileURL, &p.CountryCode,
			&p.PrimaryClanID, &p.Visibility, &p.AccountCreatedAt,
			&p.VACBanned, &p.VACBanCount, &p.GameBanCount, &p.DaysSinceLastBan, &p.EconomyBan,
			&p.ReforgerMinutes, &p.ReforgerMinutes2W, &p.GamesVisible, &p.FriendsVisible, &p.UpdatedAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return p, false, nil
	case err != nil:
		return p, false, fmt.Errorf("steam: profile: %w", err)
	}
	return p, true, nil
}

// Friends — актуальные связи игрока, СРАЗУ помеченные тем, знаем ли мы друга по своим наблюдениям.
// Это и есть главная ценность графа: «кто из его друзей тоже ходит на наши серверы».
// Известные идут первыми — остальные для человека просто цифры.
func (r *SteamRepo) Friends(ctx context.Context, steamID string) ([]steam.KnownFriend, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT e.friend_steam_id, e.friend_since,
		       p.id, COALESCE(p.current_nickname, ''), ours.last_seen
		FROM steam.friend_edge e
		LEFT JOIN player_platform_identity pl
		       ON pl.game_client_type = 'PLATFORM_PC' AND pl.platform_user_id = e.friend_steam_id
		LEFT JOIN player_identity p ON p.id = pl.player_id
		LEFT JOIN LATERAL (
			SELECT max(ss.last_seen_at) AS last_seen
			FROM player_server_session ss WHERE ss.player_id = p.id
		) ours ON true
		WHERE e.steam_id = $1 AND e.lost_at IS NULL
		ORDER BY (p.id IS NOT NULL) DESC, ours.last_seen DESC NULLS LAST, e.friend_since DESC NULLS LAST`, steamID)
	if err != nil {
		return nil, fmt.Errorf("steam: friends: %w", err)
	}
	defer rows.Close()

	var friends []steam.KnownFriend
	for rows.Next() {
		var f steam.KnownFriend
		if err := rows.Scan(&f.SteamID, &f.Since, &f.PlayerID, &f.Nickname, &f.LastSeenAt); err != nil {
			return nil, err
		}
		friends = append(friends, f)
	}
	return friends, rows.Err()
}
