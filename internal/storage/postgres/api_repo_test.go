package postgres_test

import (
	"context"
	"testing"
	"time"

	"armaplayers/internal/bohemia"
	"armaplayers/internal/httpapi"
	"armaplayers/internal/presence"
	"armaplayers/internal/storage/postgres"
)

func TestAPIRepo_endToEnd(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)

	rooms := loadRooms(t)
	if _, err := postgres.NewCatalogRepo(pool).SaveLobbyPage(ctx, t0, rooms, []byte(`{}`), t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	svc := presence.NewService(postgres.NewPresenceStore(pool), presence.Config{AbsentConfirmations: 2})
	alice := bohemia.Player{UserID: "4537e0d4-f960-46ac-bafc-a0ad390b41ea", Username: "Salat Majompski", GameClientType: "PLATFORM_PC", PlatformUserID: "76561198884181842"}
	bob := bohemia.Player{UserID: "a814bb9c-e6a2-4518-8673-16251edb1c4c", Username: "Bob", GameClientType: "PLATFORM_PSN", PlatformUserID: "1"}

	// t0: оба вошли (replay); t0+1: Alice сменила ник, Bob пропал; t0+2: Bob вышел.
	for i, obs := range []presence.Observation{
		{ServerID: 1, ObservedAt: t0, Connected: []bohemia.Player{alice, bob}, StartupReplay: true},
		{ServerID: 1, ObservedAt: t0.Add(time.Minute), Connected: []bohemia.Player{{UserID: alice.UserID, Username: "Salat", GameClientType: "PLATFORM_PC", PlatformUserID: alice.PlatformUserID}}},
		{ServerID: 1, ObservedAt: t0.Add(2 * time.Minute), Connected: []bohemia.Player{{UserID: alice.UserID, Username: "Salat", GameClientType: "PLATFORM_PC", PlatformUserID: alice.PlatformUserID}}},
	} {
		if _, err := svc.Apply(ctx, obs); err != nil {
			t.Fatalf("apply #%d: %v", i, err)
		}
	}
	api := postgres.NewAPIRepo(pool)

	// Лента без replay: NICKNAME_CHANGED + LEFT (два JOIN с replay скрыты).
	events, err := api.Events(ctx, httpapi.EventsQuery{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].Type != "PLAYER_NICKNAME_CHANGED" || events[1].Type != "PLAYER_LEFT_SERVER" {
		t.Fatalf("events: %+v", events)
	}
	left := events[1]
	if left.Nickname != "Bob" || left.ServerName == "" || left.DurationSeconds == nil || *left.DurationSeconds != 60 {
		t.Errorf("LEFT event enrichment: %+v", left)
	}
	// С replay — все четыре; курсор.
	all, _ := api.Events(ctx, httpapi.EventsQuery{Limit: 100, IncludeReplay: true})
	if len(all) != 4 {
		t.Errorf("with replay: want 4, got %d", len(all))
	}
	after, _ := api.Events(ctx, httpapi.EventsQuery{After: events[0].ID, Limit: 100})
	if len(after) != 1 || after[0].ID != left.ID {
		t.Errorf("cursor: %+v", after)
	}
	head, _ := api.EventsHead(ctx)
	if head != all[3].ID {
		t.Errorf("head: want %d, got %d", all[3].ID, head)
	}
	// Фильтр по игроку.
	bobOnly, _ := api.Events(ctx, httpapi.EventsQuery{PlayerIDs: []int64{left.PlayerID}, Limit: 100})
	if len(bobOnly) != 1 {
		t.Errorf("player filter: %+v", bobOnly)
	}

	// Поиск по старому нику находит игрока с новым текущим ником.
	found, fuzzy, err := api.SearchPlayers(ctx, "majomp", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 1 || found[0].CurrentNickname != "Salat" || len(found[0].Aliases) != 2 || found[0].Online == nil || found[0].Online.ID != 1 {
		t.Fatalf("search: %+v", found)
	}
	if fuzzy {
		t.Error("точное совпадение по подстроке не должно помечаться как fuzzy")
	}

	// Опечатка: точных совпадений нет, находится похожий — и это помечено флагом.
	typo, fuzzy, err := api.SearchPlayers(ctx, "Salta Majompksi", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(typo) != 1 || typo[0].PlayerID != found[0].PlayerID || !fuzzy {
		t.Errorf("fuzzy search: fuzzy=%v %+v", fuzzy, typo)
	}
	// Совсем чужая строка не должна находить никого даже в нечётком режиме.
	none, _, err := api.SearchPlayers(ctx, "zzzqqqxxx", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(none) != 0 {
		t.Errorf("unrelated query must find nobody: %+v", none)
	}
	if len(found[0].Platforms) != 1 || found[0].Platforms[0].ID != "76561198884181842" {
		t.Errorf("platforms: %+v", found[0].Platforms)
	}
	// Карточка Bob: оффлайн, последний сервер известен, одна сессия.
	p, ok, err := api.Player(ctx, left.PlayerID)
	if err != nil || !ok || p.Online != nil || p.LastServer == nil || p.SessionsTotal != 1 {
		t.Errorf("player card: ok=%v err=%v %+v", ok, err, p)
	}
	if _, ok, _ := api.Player(ctx, 9999); ok {
		t.Error("missing player must be not found")
	}
	// Пакетный статус: онлайн-игроки первыми.
	batch, err := api.PlayersByIDs(ctx, []int64{left.PlayerID, found[0].PlayerID})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) != 2 || batch[0].Online == nil || batch[1].Online != nil {
		t.Errorf("batch order: online first, got %+v", batch)
	}

	sessions, _ := api.PlayerSessions(ctx, left.PlayerID, 10)
	if len(sessions) != 1 || sessions[0].Status != "CLOSED_LEFT" || sessions[0].DurationSeconds != 60 || sessions[0].Nickname != "Bob" {
		t.Errorf("sessions: %+v", sessions)
	}
	servers, _ := api.Servers(ctx, false, "russian")
	if len(servers) != 1 || servers[0].Players == nil || *servers[0].Players != 128 {
		t.Errorf("servers: %+v", servers)
	}
	if tracked, _ := api.Servers(ctx, true, ""); len(tracked) != 0 {
		t.Errorf("no tracked servers expected, got %d", len(tracked))
	}
}
