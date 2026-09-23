package postgres_test

import (
	"context"
	"testing"
	"time"

	"armaplayers/internal/storage/postgres"
)

func TestRollup_tenMinuteBucketsFromTrackingOnly(t *testing.T) {
	pool := testPool(t)
	repo := postgres.NewRollupRepo(pool)
	ctx := context.Background()

	if _, err := postgres.NewCatalogRepo(pool).SaveLobbyPage(ctx, time.Now(), loadRooms(t), []byte(`{}`), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	add := func(at time.Time, source string, players, queue int) {
		t.Helper()
		if _, err := pool.Exec(ctx, `
			INSERT INTO server_observation (server_id, observed_at, source, player_count, queue_size)
			VALUES (1, $1, $2, $3, $4)`, at, source, players, queue); err != nil {
			t.Fatal(err)
		}
	}

	// Три опроса в один десятиминутный интервал и один в следующий.
	add(base.Add(1*time.Minute), "TRACKING_POLL", 10, 0)
	add(base.Add(4*time.Minute), "TRACKING_POLL", 30, 4)
	add(base.Add(9*time.Minute), "TRACKING_POLL", 20, 2)
	add(base.Add(11*time.Minute), "TRACKING_POLL", 50, 0)
	// Скан лобби в тот же интервал — в десятиминутную свёртку попасть НЕ должен.
	add(base.Add(2*time.Minute), "LOBBY_SCAN", 999, 999)

	now := base.Add(2 * time.Hour)
	if _, err := repo.RollUpLoad10m(ctx, now, 3*time.Hour); err != nil {
		t.Fatal(err)
	}

	var (
		samples          int
		avg              float64
		minP, maxP, maxQ int
	)
	if err := pool.QueryRow(ctx, `
		SELECT samples, players_avg, players_min, players_max, queue_max
		FROM server_load_10m WHERE server_id = 1 AND bucket = $1`, base).
		Scan(&samples, &avg, &minP, &maxP, &maxQ); err != nil {
		t.Fatal(err)
	}

	if samples != 3 {
		t.Errorf("наблюдений в интервале: want 3 (скан лобби не в счёт), got %d", samples)
	}
	if avg != 20 {
		t.Errorf("средний онлайн: want 20, got %v", avg)
	}
	if minP != 10 || maxP != 30 {
		t.Errorf("мин/макс онлайна: want 10/30, got %d/%d", minP, maxP)
	}
	if maxQ != 4 {
		t.Errorf("макс очередь: want 4, got %d", maxQ)
	}

	var buckets int
	pool.QueryRow(ctx, `SELECT count(*) FROM server_load_10m WHERE server_id = 1`).Scan(&buckets)
	if buckets != 2 {
		t.Errorf("интервалов: want 2, got %d", buckets)
	}

	// Повторный проход ничего не портит: свёртка идемпотентна, иначе перекрытие
	// удваивало бы счётчики на каждом часовом проходе.
	if _, err := repo.RollUpLoad10m(ctx, now, 3*time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT samples FROM server_load_10m WHERE server_id = 1 AND bucket = $1`, base).Scan(&samples); err != nil {
		t.Fatal(err)
	}
	if samples != 3 {
		t.Errorf("после повтора наблюдений: want 3, got %d", samples)
	}
}

func TestRollup_openBucketIsNotFrozen(t *testing.T) {
	pool := testPool(t)
	repo := postgres.NewRollupRepo(pool)
	ctx := context.Background()

	if _, err := postgres.NewCatalogRepo(pool).SaveLobbyPage(ctx, time.Now(), loadRooms(t), []byte(`{}`), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	base := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	if _, err := pool.Exec(ctx, `
		INSERT INTO server_observation (server_id, observed_at, source, player_count, queue_size)
		VALUES (1, $1, 'TRACKING_POLL', 10, 0)`, base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	// Момент «сейчас» внутри того же интервала: он ещё идёт, сворачивать его рано —
	// иначе половина интервала застыла бы в свёртке как полная.
	if _, err := repo.RollUpLoad10m(ctx, base.Add(5*time.Minute), 3*time.Hour); err != nil {
		t.Fatal(err)
	}

	var buckets int
	pool.QueryRow(ctx, `SELECT count(*) FROM server_load_10m`).Scan(&buckets)
	if buckets != 0 {
		t.Errorf("незакрытый интервал сворачивать нельзя, свёрнуто: %d", buckets)
	}
}

func TestRollup_dailyCountsEverySource(t *testing.T) {
	pool := testPool(t)
	repo := postgres.NewRollupRepo(pool)
	ctx := context.Background()

	if _, err := postgres.NewCatalogRepo(pool).SaveLobbyPage(ctx, time.Now(), loadRooms(t), []byte(`{}`), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	day := time.Date(2026, 9, 20, 0, 0, 0, 0, time.UTC)
	for _, o := range []struct {
		at      time.Time
		source  string
		players int
	}{
		{day.Add(3 * time.Hour), "TRACKING_POLL", 10},
		{day.Add(15 * time.Hour), "TRACKING_POLL", 60},
		{day.Add(20 * time.Hour), "LOBBY_SCAN", 40},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO server_observation (server_id, observed_at, source, player_count, queue_size)
			VALUES (1, $1, $2, $3, 0)`, o.at, o.source, o.players); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := repo.RollUpLoadDaily(ctx, day.Add(30*time.Hour), 2); err != nil {
		t.Fatal(err)
	}

	var samples, peak int
	if err := pool.QueryRow(ctx, `
		SELECT samples, players_max FROM server_load_daily WHERE server_id = 1 AND day = $1`, day).
		Scan(&samples, &peak); err != nil {
		t.Fatal(err)
	}

	// В суточную свёртку идёт ВСЁ: у неотслеживаемых серверов точки скана — единственное,
	// что о них вообще известно.
	if samples != 3 {
		t.Errorf("наблюдений за сутки: want 3, got %d", samples)
	}
	if peak != 60 {
		t.Errorf("пик онлайна: want 60, got %d", peak)
	}
}

func TestRollup_cleanupKeepsRecentAndPreservesRollups(t *testing.T) {
	pool := testPool(t)
	repo := postgres.NewRollupRepo(pool)
	ctx := context.Background()

	if _, err := postgres.NewCatalogRepo(pool).SaveLobbyPage(ctx, time.Now(), loadRooms(t), []byte(`{}`), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	old := now.Add(-30 * 24 * time.Hour)

	for i := 0; i < 5; i++ {
		if _, err := pool.Exec(ctx, `
			INSERT INTO server_observation (server_id, observed_at, source, player_count, queue_size)
			VALUES (1, $1, 'TRACKING_POLL', $2, 0)`, old.Add(time.Duration(i)*time.Minute), 10*(i+1)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO server_observation (server_id, observed_at, source, player_count, queue_size)
		VALUES (1, $1, 'TRACKING_POLL', 7, 0)`, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}

	// Порядок как в сервисе: сначала свёртка, потом чистка.
	if _, err := repo.RollUpLoad10m(ctx, now, 3*time.Hour); err != nil {
		t.Fatal(err)
	}
	deleted, err := repo.DeleteOlderThan(ctx, "server_observation", "observed_at", now.Add(-7*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 5 {
		t.Errorf("удалено: want 5, got %d", deleted)
	}

	// Считаем только свои наблюдения: подготовка данных сама пишет снимок скана лобби
	// текущим временем, и он тоже законно переживает чистку.
	var left int
	pool.QueryRow(ctx, `SELECT count(*) FROM server_observation WHERE source = 'TRACKING_POLL'`).Scan(&left)
	if left != 1 {
		t.Errorf("свежее наблюдение должно остаться, осталось: %d", left)
	}

	// Ради этого всё и затевалось: подробности удалены, а наполненность сервера за тот день
	// по-прежнему известна.
	var peak int
	if err := pool.QueryRow(ctx, `
		SELECT players_max FROM server_load_10m WHERE server_id = 1 AND bucket = $1`,
		old.Truncate(10*time.Minute)).Scan(&peak); err != nil {
		t.Fatalf("свёртка не пережила чистку: %v", err)
	}
	if peak != 50 {
		t.Errorf("пик из свёртки: want 50, got %d", peak)
	}
}

// Имена таблицы и колонки подставляются в текст запроса, поэтому берутся только из белого списка.
func TestRollup_cleanupRefusesUnknownTable(t *testing.T) {
	pool := testPool(t)
	repo := postgres.NewRollupRepo(pool)

	if _, err := repo.DeleteOlderThan(context.Background(), "player_identity", "last_seen_at", time.Now()); err == nil {
		t.Error("чистка неразрешённой таблицы должна отвергаться")
	}
}
