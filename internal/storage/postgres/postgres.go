// Package postgres — подключение к БД и применение миграций. Репозитории модулей
// живут здесь же, по файлу на модуль; домен зависит только от их интерфейсов (ADR 0002, 0003).
package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"armaplayers/internal/storage/migrations"
)

// Connect открывает пул соединений и проверяет его одним запросом.
func Connect(ctx context.Context, databaseURL string) (*pgxpool.Pool, error) {
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return nil, fmt.Errorf("postgres: parse config: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("postgres: ping: %w", err)
	}
	return pool, nil
}

// Migrate применяет все невыполненные миграции из встроенной FS. Идемпотентно:
// goose ведёт таблицу goose_db_version и пропускает уже применённые.
func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("postgres: goose dialect: %w", err)
	}

	// goose работает через database/sql; pgx умеет отдать *sql.DB поверх того же пула.
	db := stdlib.OpenDBFromPool(pool)
	defer db.Close()

	if err := goose.UpContext(ctx, db, "."); err != nil {
		return fmt.Errorf("postgres: migrate: %w", err)
	}
	return nil
}
