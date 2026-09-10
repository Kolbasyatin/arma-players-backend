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
	_, err = pool.Exec(ctx, `TRUNCATE domain_event, player_queue_session, player_server_session, player_alias, player_platform_identity, player_identity, poll_run, server_observation, server_identity_key, server, raw_payload RESTART IDENTITY CASCADE`)
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

	// Второй скан через час: тот же адрес, новый roomId и имя → тот же сервер, новых нет.
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
