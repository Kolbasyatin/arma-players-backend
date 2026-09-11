package postgres_test

import (
	"context"
	"os"
	"testing"
	"time"

	"armaplayers/internal/storage/postgres"
)

// Интеграционный тест: нужен живой Postgres. Без DATABASE_URL пропускается,
// чтобы go test ./... работал и без docker compose.
func TestMigrate(t *testing.T) {
	url := os.Getenv("DATABASE_URL_TEST")
	if url == "" {
		t.Skip("DATABASE_URL_TEST не задан — пропуск интеграционного теста")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := postgres.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	// Дважды: второй прогон должен быть no-op.
	for i := 0; i < 2; i++ {
		if err := postgres.Migrate(ctx, pool); err != nil {
			t.Fatalf("migrate #%d: %v", i+1, err)
		}
	}

	var tables int
	err = pool.QueryRow(ctx, `
		SELECT count(*) FROM information_schema.tables
		WHERE table_schema = 'public'
		  AND table_name IN ('server','server_identity_key','server_observation','poll_run','raw_payload','player_identity','player_platform_identity','player_alias','player_server_session','player_queue_session','domain_event','server_merge','mod','server_mod')`).Scan(&tables)
	if err != nil {
		t.Fatal(err)
	}
	if tables != 14 {
		t.Errorf("tables: want 14, got %d", tables)
	}

	// Требование проекта: у каждой колонки наших таблиц есть COMMENT ON.
	var uncommented int
	err = pool.QueryRow(ctx, `
		SELECT count(*)
		FROM information_schema.columns c
		JOIN pg_class cl ON cl.relname = c.table_name
		JOIN pg_namespace n ON n.oid = cl.relnamespace AND n.nspname = c.table_schema
		WHERE c.table_schema = 'public'
		  AND c.table_name IN ('server','server_identity_key','server_observation','poll_run','raw_payload','player_identity','player_platform_identity','player_alias','player_server_session','player_queue_session','domain_event','server_merge','mod','server_mod')
		  AND col_description(cl.oid, c.ordinal_position) IS NULL`).Scan(&uncommented)
	if err != nil {
		t.Fatal(err)
	}
	if uncommented != 0 {
		t.Errorf("колонок без COMMENT ON: %d", uncommented)
	}

	// Индекс под запрос «последний успешный listPlayers по серверу»: он выполняется на каждом
	// poll каждого сервера, и без индекса Postgres сортирует всю историю сервера.
	var hasIndex bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = 'poll_run_last_success_idx')`).Scan(&hasIndex); err != nil {
		t.Fatal(err)
	}
	if !hasIndex {
		t.Error("нет индекса poll_run_last_success_idx")
	}
}
