package postgres_test

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"armaplayers/internal/bohemia"
	"armaplayers/internal/catalog"
	"armaplayers/internal/observation"
	"armaplayers/internal/storage/postgres"
)

// testPool подключается к DATABASE_URL, применяет миграции и очищает наши таблицы.
func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("DATABASE_URL_TEST")
	if url == "" {
		t.Skip("DATABASE_URL_TEST не задан — пропуск интеграционного теста")
	}
	ctx := context.Background()
	pool, err := postgres.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := postgres.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `TRUNCATE domain_event, player_queue_session, player_server_session, player_alias, player_platform_identity, player_identity, poll_run, server_observation, server_identity_key, server_merge, server_mod, mod, server, raw_payload RESTART IDENTITY CASCADE`)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

func loadRooms(t *testing.T) []bohemia.Room {
	t.Helper()
	raw, err := os.ReadFile("../../bohemia/testdata/search_rooms.json")
	if err != nil {
		t.Fatal(err)
	}
	var resp bohemia.SearchRoomsResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	return resp.Rooms
}

func TestCatalogRepo_SaveLobbyPage(t *testing.T) {
	pool := testPool(t)
	repo := postgres.NewCatalogRepo(pool)
	ctx := context.Background()

	rooms := loadRooms(t)
	rooms = append(rooms, bohemia.Room{ID: "room-2", HostAddress: "10.0.0.2:2001", Name: "second"})
	t0 := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

	// Первый скан: два новых сервера.
	stats, err := repo.SaveLobbyPage(ctx, t0, rooms, []byte(`{"rooms":[]}`), t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if stats.Rooms != 2 || stats.ServersCreated != 2 {
		t.Errorf("first page: %+v", stats)
	}

	// Второй скан через час: тот же адрес, новый roomId и имя → тот же сервер (рестарт), новых нет.
	t1 := t0.Add(time.Hour)
	rooms[0].ID = "room-after-restart"
	rooms[0].Name = "renamed"
	stats, err = repo.SaveLobbyPage(ctx, t1, rooms[:1], []byte(`{}`), t1.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if stats.Rooms != 1 || stats.ServersCreated != 0 {
		t.Errorf("second page: %+v", stats)
	}

	var servers, keys, observations int
	var displayName, currentRoom string
	pool.QueryRow(ctx, `SELECT count(*) FROM server`).Scan(&servers)
	pool.QueryRow(ctx, `SELECT count(*) FROM server_identity_key WHERE server_id = 1`).Scan(&keys)
	pool.QueryRow(ctx, `SELECT count(*) FROM server_observation`).Scan(&observations)
	pool.QueryRow(ctx, `SELECT display_name, current_room_id FROM server WHERE id = 1`).Scan(&displayName, &currentRoom)

	if servers != 2 {
		t.Errorf("servers: want 2, got %d", servers)
	}
	// HOST_ADDRESS, SESSION_ID, два ROOM_ID (старый сохранён как история).
	if keys != 4 {
		t.Errorf("identity keys of server 1: want 4, got %d", keys)
	}
	if observations != 3 {
		t.Errorf("observations: want 3, got %d", observations)
	}
	if displayName != "renamed" || currentRoom != "room-after-restart" {
		t.Errorf("server current fields: %q %q", displayName, currentRoom)
	}

	// Сервер 2 не встречен во втором скане → деактивируется; сервер 1 остаётся активным.
	n, err := repo.DeactivateUnseen(ctx, t1)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("deactivated: want 1, got %d", n)
	}

	var snapshotQueue int
	pool.QueryRow(ctx, `SELECT queue_size FROM server_observation WHERE server_id = 1 ORDER BY id LIMIT 1`).Scan(&snapshotQueue)
	if snapshotQueue != 7 {
		t.Errorf("observation queue_size: want 7, got %d", snapshotQueue)
	}
}

func TestCatalogRepo_RecordPollRun(t *testing.T) {
	pool := testPool(t)
	repo := postgres.NewCatalogRepo(pool)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)
	err := repo.RecordPollRun(ctx, observation.PollRun{
		Type: observation.PollLobbyScan, StartedAt: now, FinishedAt: now.Add(time.Second),
		Status: observation.StatusAuthError, HTTPStatus: 401, ErrorMessage: "bohemia searchRooms: AUTH_ERROR (http 401)",
	})
	if err != nil {
		t.Fatal(err)
	}
	var status string
	var httpStatus int
	if err := pool.QueryRow(ctx, `SELECT status, http_status FROM poll_run`).Scan(&status, &httpStatus); err != nil {
		t.Fatal(err)
	}
	if status != "AUTH_ERROR" || httpStatus != 401 {
		t.Errorf("poll_run: %s %d", status, httpStatus)
	}
}

func TestCatalogRepo_ApplyTrackingRules(t *testing.T) {
	pool := testPool(t)
	repo := postgres.NewCatalogRepo(pool)
	ctx := context.Background()

	// Три сервера: 128 игроков (fixture), 5 игроков, 0 игроков.
	rooms := loadRooms(t)
	rooms = append(rooms,
		bohemia.Room{ID: "r2", HostAddress: "10.0.0.2:2001", Name: "small", PlayerCount: 5},
		bohemia.Room{ID: "r3", HostAddress: "10.0.0.3:2001", Name: "empty", PlayerCount: 0},
	)
	now := time.Now().UTC()
	if _, err := repo.SaveLobbyPage(ctx, now, rooms, []byte(`{}`), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	// MANUAL: пустой сервер; AUTO: ≥10 игроков, max 5 → только большой.
	st, err := repo.ApplyTrackingRules(ctx, catalog.TrackingRules{ManualHosts: []string{"10.0.0.3:2001"}, AutoMinPlayers: 10, AutoMaxServers: 5})
	if err != nil {
		t.Fatal(err)
	}
	if st.Manual != 1 || st.Auto != 1 || st.Disabled != 0 {
		t.Errorf("first apply: %+v", st)
	}

	// Большой сервер «перезагрузился»: свежий снимок с 0 игроков. Пик за окно всё равно 128 → остаётся.
	rooms[0].PlayerCount = 0
	if _, err := repo.SaveLobbyPage(ctx, now.Add(time.Minute), rooms[:1], []byte(`{}`), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	st, err = repo.ApplyTrackingRules(ctx, catalog.TrackingRules{ManualHosts: []string{"10.0.0.3:2001"}, AutoMinPlayers: 10, AutoMaxServers: 5})
	if err != nil {
		t.Fatal(err)
	}
	if st.Auto != 1 || st.Disabled != 0 {
		t.Errorf("after restart snapshot: %+v (server must stay tracked by 7-day peak)", st)
	}

	// Убрали ручной, порог подняли до 200 → оба выключаются.
	st, err = repo.ApplyTrackingRules(ctx, catalog.TrackingRules{AutoMinPlayers: 200, AutoMaxServers: 5})
	if err != nil {
		t.Fatal(err)
	}
	if st.Manual != 0 || st.Auto != 0 || st.Disabled != 2 {
		t.Errorf("second apply: %+v", st)
	}
	var enabled int
	pool.QueryRow(ctx, `SELECT count(*) FROM server WHERE tracking_enabled`).Scan(&enabled)
	if enabled != 0 {
		t.Errorf("enabled after disable: %d", enabled)
	}
}

func TestCatalogRepo_DeleteExpiredRawPayloads(t *testing.T) {
	pool := testPool(t)
	repo := postgres.NewCatalogRepo(pool)
	ctx := context.Background()
	now := time.Now().UTC()

	if _, err := repo.SaveLobbyPage(ctx, now.Add(-2*time.Hour), loadRooms(t), []byte(`{"old":true}`), now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SaveLobbyPage(ctx, now, loadRooms(t), []byte(`{"fresh":true}`), now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	n, err := repo.DeleteExpiredRawPayloads(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("deleted: want 1, got %d", n)
	}
	// Снимок, ссылавшийся на удалённый payload, остался, ссылка обнулилась (ON DELETE SET NULL).
	var orphaned int
	pool.QueryRow(ctx, `SELECT count(*) FROM server_observation WHERE raw_payload_id IS NULL`).Scan(&orphaned)
	if orphaned != 1 {
		t.Errorf("observations with NULL raw_payload_id: want 1, got %d", orphaned)
	}
}

func TestCatalogRepo_resolveByRoomIDWhenAddressChanges(t *testing.T) {
	pool := testPool(t)
	repo := postgres.NewCatalogRepo(pool)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 11, 6, 0, 0, 0, time.UTC)

	room := bohemia.Room{ID: "room-A", HostAddress: "69.67.175.16:2008", Name: "WCS NA6", SessionID: "sess-1"}
	if _, err := repo.SaveLobbyPage(ctx, t0, []bohemia.Room{room}, []byte(`{}`), t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// Переезд на другой порт с тем же roomId → тот же сервер, адрес обновлён, новой записи нет.
	room.HostAddress = "69.67.175.16:2010"
	stats, err := repo.SaveLobbyPage(ctx, t0.Add(2*time.Hour), []bohemia.Room{room}, []byte(`{}`), t0.Add(3*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if stats.ServersCreated != 0 {
		t.Fatalf("address change with same roomId must not create a server: %+v", stats)
	}
	var servers, addrKeys int
	var addr string
	pool.QueryRow(ctx, `SELECT count(*) FROM server`).Scan(&servers)
	pool.QueryRow(ctx, `SELECT current_host_address FROM server WHERE id = 1`).Scan(&addr)
	pool.QueryRow(ctx, `SELECT count(*) FROM server_identity_key WHERE server_id = 1 AND key_type = 'HOST_ADDRESS'`).Scan(&addrKeys)
	if servers != 1 || addr != "69.67.175.16:2010" || addrKeys != 2 {
		t.Errorf("servers=%d addr=%s addrKeys=%d", servers, addr, addrKeys)
	}
}

func TestCatalogRepo_MergeDuplicateRooms(t *testing.T) {
	pool := testPool(t)
	repo := postgres.NewCatalogRepo(pool)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 11, 6, 0, 0, 0, time.UTC)

	// Имитация дублей, созданных до резолюции по roomId: две записи с одним ROOM_ID.
	_, err := pool.Exec(ctx, `
		INSERT INTO server (display_name, current_host_address, current_room_id, first_seen_at, last_seen_at, tracking_enabled, tracking_source, active)
		VALUES ('old', '1.1.1.1:2001', 'room-X', $1, $1, false, NULL, false),
		       ('new', '1.1.1.1:2002', 'room-X', $2, $2, true, 'AUTO', true)`, t0, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	_, err = pool.Exec(ctx, `
		INSERT INTO server_identity_key (server_id, key_type, key_value, first_seen_at, last_seen_at)
		VALUES (1, 'ROOM_ID', 'room-X', $1, $1), (2, 'ROOM_ID', 'room-X', $2, $2)`, t0, t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	n, err := repo.MergeDuplicateRooms(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("merged: want 1, got %d", n)
	}
	var mergedInto *int64
	var keepAddr, keepSource string
	var keepTracked, keepActive, victimTracked bool
	pool.QueryRow(ctx, `SELECT merged_into_server_id, tracking_enabled FROM server WHERE id = 2`).Scan(&mergedInto, &victimTracked)
	pool.QueryRow(ctx, `SELECT current_host_address, tracking_enabled, COALESCE(tracking_source,''), active FROM server WHERE id = 1`).Scan(&keepAddr, &keepTracked, &keepSource, &keepActive)
	if mergedInto == nil || *mergedInto != 1 || victimTracked {
		t.Errorf("victim: merged_into=%v tracked=%v", mergedInto, victimTracked)
	}
	if keepAddr != "1.1.1.1:2002" || !keepTracked || keepSource != "AUTO" || !keepActive {
		t.Errorf("canonical: addr=%s tracked=%v source=%s active=%v", keepAddr, keepTracked, keepSource, keepActive)
	}
	// Повторный вызов — no-op.
	if n, _ := repo.MergeDuplicateRooms(ctx); n != 0 {
		t.Errorf("second merge must be no-op, got %d", n)
	}
	// Резолюция по любому из адресов и по roomId ведёт в канонический сервер 1.
	stats, err := repo.SaveLobbyPage(ctx, t0.Add(2*time.Hour), []bohemia.Room{{ID: "room-X", HostAddress: "1.1.1.1:2002", Name: "new"}}, []byte(`{}`), t0.Add(3*time.Hour))
	if err != nil || stats.ServersCreated != 0 {
		t.Errorf("resolve after merge: %+v %v", stats, err)
	}
	var obsServer int64
	pool.QueryRow(ctx, `SELECT server_id FROM server_observation ORDER BY id DESC LIMIT 1`).Scan(&obsServer)
	if obsServer != 1 {
		t.Errorf("observation must go to canonical server 1, got %d", obsServer)
	}
}

func TestCatalogRepo_modsPersisted(t *testing.T) {
	pool := testPool(t)
	repo := postgres.NewCatalogRepo(pool)
	ctx := context.Background()
	t0 := time.Date(2026, 9, 11, 6, 0, 0, 0, time.UTC)

	rooms := loadRooms(t) // fixture: 3 мода
	if len(rooms[0].Mods) != 3 {
		t.Fatalf("fixture must contain 3 mods, got %d", len(rooms[0].Mods))
	}
	for i := 0; i < 3; i++ { // три скана с неизменным набором
		if _, err := repo.SaveLobbyPage(ctx, t0.Add(time.Duration(i)*time.Hour), rooms, []byte(`{}`), t0.Add(24*time.Hour)); err != nil {
			t.Fatal(err)
		}
	}
	var mods, serverMods int
	var hash *string
	pool.QueryRow(ctx, `SELECT count(*) FROM mod`).Scan(&mods)
	pool.QueryRow(ctx, `SELECT count(*) FROM server_mod WHERE server_id = 1`).Scan(&serverMods)
	pool.QueryRow(ctx, `SELECT mod_set_hash FROM server WHERE id = 1`).Scan(&hash)
	if mods != 3 || serverMods != 3 || hash == nil {
		t.Fatalf("after 3 identical scans: mods=%d server_mods=%d hash=%v", mods, serverMods, hash)
	}

	// Обновили версию одного мода → новая строка истории, старая осталась, хеш сменился.
	rooms[0].Mods[0].Version = "9.9.9"
	if _, err := repo.SaveLobbyPage(ctx, t0.Add(4*time.Hour), rooms, []byte(`{}`), t0.Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var newHash *string
	pool.QueryRow(ctx, `SELECT count(*) FROM server_mod WHERE server_id = 1`).Scan(&serverMods)
	pool.QueryRow(ctx, `SELECT mod_set_hash FROM server WHERE id = 1`).Scan(&newHash)
	if serverMods != 4 || newHash == nil || *newHash == *hash {
		t.Errorf("after version change: server_mods=%d hash changed=%v", serverMods, newHash != nil && *newHash != *hash)
	}

	var modCount int
	var clientTypes []string
	var queueType, hosted string
	pool.QueryRow(ctx, `SELECT mod_count, supported_game_client_types, queue_type, hosted_scenario_mod_id FROM server_observation WHERE server_id = 1 ORDER BY id DESC LIMIT 1`).Scan(&modCount, &clientTypes, &queueType, &hosted)
	if modCount != 3 || len(clientTypes) != 3 || queueType != "REGULAR" || hosted == "" {
		t.Errorf("observation extra fields: mods=%d types=%v queue=%q hosted=%q", modCount, clientTypes, queueType, hosted)
	}
}
