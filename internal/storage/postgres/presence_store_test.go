package postgres_test

import (
	"context"
	"testing"
	"time"

	"armaplayers/internal/bohemia"
	"armaplayers/internal/presence"
	"armaplayers/internal/storage/postgres"
)

func TestPresenceStore_endToEnd(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	// Нужен сервер: берём из fixture каталога.
	if _, err := postgres.NewCatalogRepo(pool).SaveLobbyPage(ctx, time.Now(), loadRooms(t), []byte(`{}`), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	svc := presence.NewService(postgres.NewPresenceStore(pool), presence.Config{AbsentConfirmations: 2, MaxGap: 15 * time.Minute})

	t0 := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	alice := bohemia.Player{UserID: "4537e0d4-f960-46ac-bafc-a0ad390b41ea", Username: "Alice", GameClientType: "PLATFORM_PC", PlatformUserID: "76561198884181842"}
	bob := bohemia.Player{UserID: "a814bb9c-e6a2-4518-8673-16251edb1c4c", Username: "Bob", GameClientType: "PLATFORM_PSN", PlatformUserID: "1988494528553747446"}

	// poll 1: оба в игре.
	res, err := svc.Apply(ctx, presence.Observation{ServerID: 1, ObservedAt: t0, Connected: []bohemia.Player{alice, bob}, StartupReplay: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Joined != 2 || res.NewPlayers != 2 {
		t.Fatalf("poll1: %+v", res)
	}

	// poll 2: Alice сменила ник, Bob пропал.
	alice.Username = "Alicia"
	res, err = svc.Apply(ctx, presence.Observation{ServerID: 1, ObservedAt: t0.Add(time.Minute), Connected: []bohemia.Player{alice}})
	if err != nil {
		t.Fatal(err)
	}
	if res.NicknameChanges != 1 || res.SuspectedGone != 1 {
		t.Fatalf("poll2: %+v", res)
	}

	// poll 3: Bob отсутствует второй раз → выход.
	res, err = svc.Apply(ctx, presence.Observation{ServerID: 1, ObservedAt: t0.Add(2 * time.Minute), Connected: []bohemia.Player{alice}})
	if err != nil {
		t.Fatal(err)
	}
	if res.Left != 1 {
		t.Fatalf("poll3: %+v", res)
	}

	var players, aliases, platforms, openSessions, closedLeft, events int
	var aliceNick, aliceSessionNick string
	pool.QueryRow(ctx, `SELECT count(*) FROM player_identity`).Scan(&players)
	pool.QueryRow(ctx, `SELECT count(*) FROM player_alias`).Scan(&aliases)
	pool.QueryRow(ctx, `SELECT count(*) FROM player_platform_identity WHERE game_client_type = 'PLATFORM_PSN'`).Scan(&platforms)
	pool.QueryRow(ctx, `SELECT count(*) FROM player_server_session WHERE ended_at IS NULL`).Scan(&openSessions)
	pool.QueryRow(ctx, `SELECT count(*) FROM player_server_session WHERE status = 'CLOSED_LEFT' AND ended_at = $1`, t0.Add(time.Minute)).Scan(&closedLeft)
	pool.QueryRow(ctx, `SELECT count(*) FROM domain_event`).Scan(&events)
	pool.QueryRow(ctx, `SELECT current_nickname FROM player_identity WHERE bohemia_user_id = $1`, alice.UserID).Scan(&aliceNick)
	pool.QueryRow(ctx, `SELECT s.nickname FROM player_server_session s JOIN player_identity p ON p.id = s.player_id WHERE p.bohemia_user_id = $1`, alice.UserID).Scan(&aliceSessionNick)
	if aliceSessionNick != "Alicia" {
		t.Errorf("session nickname: want Alicia, got %q", aliceSessionNick)
	}

	if players != 2 || aliases != 3 || platforms != 1 || openSessions != 1 || closedLeft != 1 || aliceNick != "Alicia" {
		t.Errorf("state: players=%d aliases=%d psn=%d open=%d closedLeft=%d aliceNick=%q", players, aliases, platforms, openSessions, closedLeft, aliceNick)
	}
	// 2 JOIN (startup_replay) + 1 NICKNAME_CHANGED + 1 LEFT.
	if events != 4 {
		t.Errorf("events: want 4, got %d", events)
	}
	var replayJoins int
	pool.QueryRow(ctx, `SELECT count(*) FROM domain_event WHERE event_type = 'PLAYER_JOINED_SERVER' AND startup_replay`).Scan(&replayJoins)
	if replayJoins != 2 {
		t.Errorf("startup_replay joins: want 2, got %d", replayJoins)
	}
}

// Главный тест против распухания: повторные наблюдения не должны переписывать строки игрока.
// Раньше каждый poll обновлял player_identity, player_alias и player_platform_identity, и на
// 150 тысячах строк накопилось 45 миллионов обновлений на таблицу — по 4 КБ мусора на строку.
func TestPresenceStore_repeatedObservationsDoNotRewritePlayerRows(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	if _, err := postgres.NewCatalogRepo(pool).SaveLobbyPage(ctx, time.Now(), loadRooms(t), []byte(`{}`), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	svc := presence.NewService(postgres.NewPresenceStore(pool), presence.Config{AbsentConfirmations: 2})

	t0 := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	alice := bohemia.Player{UserID: "4537e0d4-f960-46ac-bafc-a0ad390b41ea", Username: "Alice", GameClientType: "PLATFORM_PC", PlatformUserID: "76561198884181842"}

	observe := func(at time.Time, p bohemia.Player) {
		t.Helper()
		if _, err := svc.Apply(ctx, presence.Observation{ServerID: 1, ObservedAt: at, Connected: []bohemia.Player{p}}); err != nil {
			t.Fatal(err)
		}
	}

	counts := func() (aliasCount, platformCount int, identitySeen, sessionSeen time.Time) {
		t.Helper()
		if err := pool.QueryRow(ctx, `
			SELECT a.observation_count, pl.observation_count, p.last_seen_at, ss.last_seen_at
			FROM player_identity p
			JOIN player_alias a ON a.player_id = p.id
			JOIN player_platform_identity pl ON pl.player_id = p.id
			JOIN player_server_session ss ON ss.player_id = p.id
			WHERE p.bohemia_user_id = $1`, alice.UserID).
			Scan(&aliasCount, &platformCount, &identitySeen, &sessionSeen); err != nil {
			t.Fatal(err)
		}
		return
	}

	// Первое наблюдение заводит строки.
	observe(t0, alice)
	aliasCount, platformCount, identitySeen, sessionSeen := counts()
	if aliasCount != 1 || platformCount != 1 || !identitySeen.Equal(t0) || !sessionSeen.Equal(t0) {
		t.Fatalf("после первого наблюдения: alias=%d platform=%d identity=%s session=%s", aliasCount, platformCount, identitySeen, sessionSeen)
	}

	// Десять наблюдений в пределах часа: строки игрока трогать НЕЛЬЗЯ, сессию — обязательно.
	for i := 1; i <= 10; i++ {
		observe(t0.Add(time.Duration(i)*2*time.Minute), alice)
	}
	aliasCount, platformCount, identitySeen, sessionSeen = counts()
	if aliasCount != 1 || platformCount != 1 {
		t.Errorf("счётчики не должны расти внутри интервала: alias=%d platform=%d", aliasCount, platformCount)
	}
	if !identitySeen.Equal(t0) {
		t.Errorf("player_identity.last_seen_at не должен переписываться внутри интервала: %s", identitySeen)
	}
	if !sessionSeen.Equal(t0.Add(20 * time.Minute)) {
		t.Errorf("сессия обязана обновляться каждым наблюдением, получено %s", sessionSeen)
	}

	// За пределами интервала отметка обновляется — иначе она перестала бы что-либо значить.
	observe(t0.Add(2*time.Hour), alice)
	aliasCount, platformCount, identitySeen, _ = counts()
	if aliasCount != 2 || platformCount != 2 || !identitySeen.Equal(t0.Add(2*time.Hour)) {
		t.Errorf("за пределами интервала: alias=%d platform=%d identity=%s", aliasCount, platformCount, identitySeen)
	}

	// Смена ника видна СРАЗУ, независимо от интервала: это изменение по сути, а не отметка времени.
	renamed := alice
	renamed.Username = "Alicia"
	observe(t0.Add(2*time.Hour+2*time.Minute), renamed)

	var current string
	var aliases int
	pool.QueryRow(ctx, `SELECT current_nickname FROM player_identity WHERE bohemia_user_id = $1`, alice.UserID).Scan(&current)
	pool.QueryRow(ctx, `SELECT count(*) FROM player_alias`).Scan(&aliases)
	if current != "Alicia" || aliases != 2 {
		t.Errorf("смена ника должна применяться сразу: current=%q aliases=%d", current, aliases)
	}
	var renameEvents int
	pool.QueryRow(ctx, `SELECT count(*) FROM domain_event WHERE event_type = 'PLAYER_NICKNAME_CHANGED'`).Scan(&renameEvents)
	if renameEvents != 1 {
		t.Errorf("событие переименования: ожидалось 1, получено %d", renameEvents)
	}
}
