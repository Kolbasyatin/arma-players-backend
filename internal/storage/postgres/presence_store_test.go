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
	var aliceNick string
	pool.QueryRow(ctx, `SELECT count(*) FROM player_identity`).Scan(&players)
	pool.QueryRow(ctx, `SELECT count(*) FROM player_alias`).Scan(&aliases)
	pool.QueryRow(ctx, `SELECT count(*) FROM player_platform_identity WHERE game_client_type = 'PLATFORM_PSN'`).Scan(&platforms)
	pool.QueryRow(ctx, `SELECT count(*) FROM player_server_session WHERE ended_at IS NULL`).Scan(&openSessions)
	pool.QueryRow(ctx, `SELECT count(*) FROM player_server_session WHERE status = 'CLOSED_LEFT' AND ended_at = $1`, t0.Add(time.Minute)).Scan(&closedLeft)
	pool.QueryRow(ctx, `SELECT count(*) FROM domain_event`).Scan(&events)
	pool.QueryRow(ctx, `SELECT current_nickname FROM player_identity WHERE bohemia_user_id = $1`, alice.UserID).Scan(&aliceNick)

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
