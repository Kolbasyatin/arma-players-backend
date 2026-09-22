package postgres_test

import (
	"context"
	"testing"
	"time"

	"armaplayers/internal/bohemia"
	"armaplayers/internal/presence"
	"armaplayers/internal/steam"
	"armaplayers/internal/storage/postgres"
)

func TestSteamRepo_snapshotWrittenOnlyWhenChanged(t *testing.T) {
	pool := testPool(t)
	repo := postgres.NewSteamRepo(pool)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)

	const id = "76561198884181842"
	body := []byte(`{"response":{"game_count":54}}`)

	// Первый снимок пишется всегда.
	written, err := repo.SaveSnapshot(ctx, id, steam.SourceGames, t0, body)
	if err != nil {
		t.Fatal(err)
	}
	if !written {
		t.Fatal("первый снимок должен быть записан")
	}

	// Десять одинаковых ответов подряд не должны оставить в базе ни одной новой строки:
	// это единственное, что стоит между нами и повторением истории raw_payload.
	for i := 1; i <= 10; i++ {
		written, err := repo.SaveSnapshot(ctx, id, steam.SourceGames, t0.Add(time.Duration(i)*time.Hour), body)
		if err != nil {
			t.Fatal(err)
		}
		if written {
			t.Fatalf("повтор %d: записан снимок, хотя содержимое не менялось", i)
		}
	}

	// Изменилось — записали.
	if written, err = repo.SaveSnapshot(ctx, id, steam.SourceGames, t0.Add(11*time.Hour), []byte(`{"response":{"game_count":55}}`)); err != nil || !written {
		t.Fatalf("изменившийся ответ должен быть записан: written=%v err=%v", written, err)
	}

	// Другой источник с тем же телом — своя история, свой хеш.
	if written, err = repo.SaveSnapshot(ctx, id, steam.SourceBans, t0, body); err != nil || !written {
		t.Fatalf("другой источник должен писаться независимо: written=%v err=%v", written, err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM steam.snapshot WHERE steam_id = $1`, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Errorf("снимков в базе: want 3, got %d", count)
	}
}

func TestSteamRepo_profileKeepsKnownValuesWhenSourceMissing(t *testing.T) {
	pool := testPool(t)
	repo := postgres.NewSteamRepo(pool)
	ctx := context.Background()
	const id = "76561198698316843"

	minutes := 51648
	vac := false
	full := steam.Profile{
		SteamID: id, PersonaName: "Шустрый", EconomyBan: "none",
		ReforgerMinutes: &minutes, VACBanned: &vac, GamesVisible: true, FriendsVisible: true,
	}
	if err := repo.SaveProfile(ctx, full); err != nil {
		t.Fatal(err)
	}

	// Следующий обход: Valve не ответил ни по одному источнику. Затирать накопленное нельзя,
	// иначе один сбой у чужого сервиса обнуляет всё, что мы знали об игроке.
	if err := repo.SaveProfile(ctx, steam.Profile{SteamID: id}); err != nil {
		t.Fatal(err)
	}

	var (
		persona string
		mins    *int
		banned  *bool
		visible bool
	)
	if err := pool.QueryRow(ctx, `
		SELECT persona_name, reforger_minutes, vac_banned, games_visible
		FROM steam.profile WHERE steam_id = $1`, id).Scan(&persona, &mins, &banned, &visible); err != nil {
		t.Fatal(err)
	}
	if persona != "Шустрый" {
		t.Errorf("ник затёрт: %q", persona)
	}
	if mins == nil || *mins != 51648 {
		t.Errorf("наигранное затёрто: %v", mins)
	}
	if banned == nil || *banned {
		t.Errorf("баны затёрты: %v", banned)
	}
	// А вот признаки видимости обязаны отражать ПОСЛЕДНЮЮ попытку: игрок закрыл профиль —
	// это новость, а не потеря данных.
	if visible {
		t.Error("games_visible должен стать false: в последнем ответе игр не было")
	}
}

func TestSteamRepo_friendsMarkedLostNotDeleted(t *testing.T) {
	pool := testPool(t)
	repo := postgres.NewSteamRepo(pool)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 22, 10, 0, 0, 0, time.UTC)
	const id = "76561198698316843"

	since := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	first := []steam.Friend{{SteamID: "111", Since: &since}, {SteamID: "222"}, {SteamID: "333"}}
	if err := repo.SaveFriends(ctx, id, first, t0); err != nil {
		t.Fatal(err)
	}

	// Расфрендились с 222, появился 444.
	second := []steam.Friend{{SteamID: "111"}, {SteamID: "333"}, {SteamID: "444"}}
	if err := repo.SaveFriends(ctx, id, second, t0.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	rows, err := pool.Query(ctx, `
		SELECT friend_steam_id, lost_at IS NULL FROM steam.friend_edge
		WHERE steam_id = $1 ORDER BY friend_steam_id`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	got := map[string]bool{}
	for rows.Next() {
		var friend string
		var active bool
		if err := rows.Scan(&friend, &active); err != nil {
			t.Fatal(err)
		}
		got[friend] = active
	}

	want := map[string]bool{"111": true, "222": false, "333": true, "444": true}
	if len(got) != len(want) {
		t.Fatalf("рёбер: want %v, got %v", want, got)
	}
	for friend, active := range want {
		if got[friend] != active {
			t.Errorf("друг %s: активен want %v, got %v", friend, active, got[friend])
		}
	}

	// friend_since не должен потеряться: во втором ответе Valve его не прислал.
	var since2 *time.Time
	if err := pool.QueryRow(ctx,
		`SELECT friend_since FROM steam.friend_edge WHERE steam_id = $1 AND friend_steam_id = '111'`, id).Scan(&since2); err != nil {
		t.Fatal(err)
	}
	if since2 == nil || !since2.Equal(since) {
		t.Errorf("friend_since затёрт: %v", since2)
	}
}

func TestSteamRepo_watchlistOrderAndStaleness(t *testing.T) {
	pool := testPool(t)
	repo := postgres.NewSteamRepo(pool)
	ctx := context.Background()
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

	policy := steam.WatchlistPolicy{
		ActiveWindow:    7 * 24 * time.Hour,
		ActiveStaleness: 24 * time.Hour,
		IdleStaleness:   7 * 24 * time.Hour,
	}

	for _, id := range []string{"1000", "2000", "3000"} {
		if err := repo.AddToWatchlist(ctx, id, "test", "заметка"); err != nil {
			t.Fatal(err)
		}
	}

	// 1000 опрошен только что, 2000 — сутки назад, 3000 не опрашивался никогда.
	if err := repo.MarkTry(ctx, "1000", now.Add(-time.Minute), ""); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkTry(ctx, "2000", now.Add(-25*time.Hour), "HTTP 500"); err != nil {
		t.Fatal(err)
	}

	ids, err := repo.Watchlist(ctx, policy, now, 10)
	if err != nil {
		t.Fatal(err)
	}

	// Никогда не опрошенный идёт первым, свежий не попадает вовсе.
	// 2000 никем у нас не наблюдался, поэтому он неактивен — и суток ему мало.
	want := []string{"3000"}
	if len(ids) != len(want) || ids[0] != want[0] {
		t.Fatalf("очередь: want %v, got %v", want, ids)
	}

	var errText string
	if err := pool.QueryRow(ctx, `SELECT last_error FROM steam.watchlist WHERE steam_id = '2000'`).Scan(&errText); err != nil {
		t.Fatal(err)
	}
	if errText != "HTTP 500" {
		t.Errorf("ошибка попытки не сохранена: %q", errText)
	}
}

// Ключевая проверка разной частоты: два одинаково давно опрошенных игрока, но один играл
// на наших серверах вчера, а второй не появлялся вовсе. Первый должен попасть в очередь,
// второй — подождать до своего, более редкого срока.
func TestSteamRepo_watchlistActivePlayerRefreshedMoreOften(t *testing.T) {
	pool := testPool(t)
	repo := postgres.NewSteamRepo(pool)
	ctx := context.Background()
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

	policy := steam.WatchlistPolicy{
		ActiveWindow:    7 * 24 * time.Hour,
		ActiveStaleness: 24 * time.Hour,
		IdleStaleness:   7 * 24 * time.Hour,
	}

	const activeSteamID = "76561198884181842"
	const idleSteamID = "76561198698316843"

	// Активному игроку заводим настоящую цепочку: игрок -> платформенный id -> сессия вчера.
	if _, err := postgres.NewCatalogRepo(pool).SaveLobbyPage(ctx, now, loadRooms(t), []byte(`{}`), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	svc := presence.NewService(postgres.NewPresenceStore(pool), presence.Config{AbsentConfirmations: 2})
	if _, err := svc.Apply(ctx, presence.Observation{
		ServerID:   1,
		ObservedAt: now.Add(-24 * time.Hour),
		Connected: []bohemia.Player{{
			UserID: "4537e0d4-f960-46ac-bafc-a0ad390b41ea", Username: "Active",
			GameClientType: "PLATFORM_PC", PlatformUserID: activeSteamID,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	for _, id := range []string{activeSteamID, idleSteamID} {
		if err := repo.AddToWatchlist(ctx, id, "test", ""); err != nil {
			t.Fatal(err)
		}
		// Оба опрошены ровно двое суток назад: активному этого много, неактивному — нет.
		if err := repo.MarkTry(ctx, id, now.Add(-48*time.Hour), ""); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := repo.Watchlist(ctx, policy, now, 10)
	if err != nil {
		t.Fatal(err)
	}

	if len(ids) != 1 || ids[0] != activeSteamID {
		t.Fatalf("в очередь должен попасть только активный игрок: want [%s], got %v", activeSteamID, ids)
	}
}
