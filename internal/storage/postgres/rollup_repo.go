package postgres

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// RollupRepo — свёртки нагрузки и чистка растущих таблиц (AGENTS §12).
type RollupRepo struct {
	pool *pgxpool.Pool
}

func NewRollupRepo(pool *pgxpool.Pool) *RollupRepo { return &RollupRepo{pool: pool} }

// deleteBatch — сколько строк удаляем за один заход. Пачками, а не одной командой:
// удаление миллионов строк держит транзакцию часами и полностью откатывается при рестарте.
// Именно так однажды выжили 468 тысяч просроченных строк сырья.
const deleteBatch = 20_000

// bucketSeconds — шаг детальной свёртки.
const bucketSeconds = 600

// RollUpLoad10m сворачивает наблюдения ОПРОСА в десятиминутные интервалы.
//
// Источник только TRACKING_POLL: суточный скан лобби даёт одну-две точки в сутки на сервер,
// и десятиминутные интервалы для него бессмысленны — он попадает в суточную свёртку.
//
// Диапазон вычисляется в самом запросе: продолжаем с последнего свёрнутого интервала,
// а если свёрток ещё нет — с самого начала истории. Так первый запуск досчитывает всё
// накопленное, а последующие делают мало работы, и отдельная таблица водяного знака не нужна.
// overlap пересчитывает хвост заново: он идемпотентен (ON CONFLICT DO UPDATE) и чинит
// интервалы, свёрнутые наполовину из-за рестарта посреди периода.
func (r *RollupRepo) RollUpLoad10m(ctx context.Context, now time.Time, overlap time.Duration) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		WITH bounds AS (
			SELECT COALESCE(
			           (SELECT max(bucket) FROM server_load_10m) - $2::interval,
			           (SELECT min(observed_at) FROM server_observation)
			       ) AS from_at,
			       -- Только ЗАКРЫТЫЕ интервалы: незакрытый свернулся бы наполовину
			       -- и остался бы таким до следующего перекрытия.
			       to_timestamp(floor(extract(epoch FROM $1::timestamptz) / $3) * $3) AS to_at
		)
		INSERT INTO server_load_10m (server_id, bucket, samples, players_avg, players_min, players_max, queue_avg, queue_max)
		SELECT o.server_id,
		       to_timestamp(floor(extract(epoch FROM o.observed_at) / $3) * $3),
		       count(*),
		       round(avg(o.player_count), 2), min(o.player_count), max(o.player_count),
		       round(avg(o.queue_size), 2), max(o.queue_size)
		FROM server_observation o, bounds b
		WHERE o.source = 'TRACKING_POLL'
		  AND b.from_at IS NOT NULL
		  AND o.observed_at >= b.from_at
		  AND o.observed_at < b.to_at
		GROUP BY 1, 2
		ON CONFLICT (server_id, bucket) DO UPDATE SET
			samples     = EXCLUDED.samples,
			players_avg = EXCLUDED.players_avg,
			players_min = EXCLUDED.players_min,
			players_max = EXCLUDED.players_max,
			queue_avg   = EXCLUDED.queue_avg,
			queue_max   = EXCLUDED.queue_max`,
		now, overlap, bucketSeconds)
	if err != nil {
		return 0, fmt.Errorf("rollup 10m: %w", err)
	}
	return tag.RowsAffected(), nil
}

// RollUpLoadDaily — суточная свёртка по ВСЕМ серверам, включая неотслеживаемые:
// по ним есть только точки скана лобби, и это единственное, что о них вообще известно.
//
// Пересчитываются последние overlapDays суток целиком: текущие сутки ещё не закончились,
// и их строка обязана обновляться, а не замереть на утреннем значении.
func (r *RollupRepo) RollUpLoadDaily(ctx context.Context, now time.Time, overlapDays int) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		WITH bounds AS (
			SELECT COALESCE(
			           (SELECT max(day) FROM server_load_daily) - make_interval(days => $2),
			           (SELECT min(observed_at)::date FROM server_observation)
			       ) AS from_day
		)
		INSERT INTO server_load_daily (server_id, day, samples, players_avg, players_min, players_max, queue_max)
		SELECT o.server_id, (o.observed_at AT TIME ZONE 'UTC')::date,
		       count(*), round(avg(o.player_count), 2), min(o.player_count), max(o.player_count), max(o.queue_size)
		FROM server_observation o, bounds b
		WHERE b.from_day IS NOT NULL
		  AND o.observed_at >= b.from_day
		  -- Верхняя граница нужна не для полноты, а от кривых часов: наблюдение из будущего
		  -- создало бы строку за сутки, которые ещё не наступили.
		  AND o.observed_at <= $1::timestamptz
		GROUP BY 1, 2
		ON CONFLICT (server_id, day) DO UPDATE SET
			samples     = EXCLUDED.samples,
			players_avg = EXCLUDED.players_avg,
			players_min = EXCLUDED.players_min,
			players_max = EXCLUDED.players_max,
			queue_max   = EXCLUDED.queue_max`,
		now, overlapDays)
	if err != nil {
		return 0, fmt.Errorf("rollup daily: %w", err)
	}
	return tag.RowsAffected(), nil
}

// RollUpPollRuns — суточная доля успешных обращений к Bohemia. Подробный журнал живёт недолго,
// а деградацию чужого API видно только на длинном горизонте.
func (r *RollupRepo) RollUpPollRuns(ctx context.Context, now time.Time, overlapDays int) (int64, error) {
	tag, err := r.pool.Exec(ctx, `
		WITH bounds AS (
			SELECT COALESCE(
			           (SELECT max(day) FROM poll_run_daily) - make_interval(days => $2),
			           (SELECT min(finished_at)::date FROM poll_run)
			       ) AS from_day
		)
		INSERT INTO poll_run_daily (day, poll_type, status, runs)
		SELECT (p.finished_at AT TIME ZONE 'UTC')::date, p.poll_type, p.status, count(*)
		FROM poll_run p, bounds b
		WHERE b.from_day IS NOT NULL
		  AND p.finished_at >= b.from_day
		  AND p.finished_at <= $1::timestamptz
		GROUP BY 1, 2, 3
		ON CONFLICT (day, poll_type, status) DO UPDATE SET runs = EXCLUDED.runs`,
		now, overlapDays)
	if err != nil {
		return 0, fmt.Errorf("rollup poll runs: %w", err)
	}
	return tag.RowsAffected(), nil
}

// DeleteOlderThan удаляет старые строки пачками. Возвращает сколько удалено.
//
// Вызывать ТОЛЬКО после свёртки: строки старше срока хранения к этому моменту уже посчитаны,
// потому что свёртка доходит до текущего момента, а срок хранения измеряется сутками.
func (r *RollupRepo) DeleteOlderThan(ctx context.Context, table, timeColumn string, before time.Time) (int64, error) {
	// Имена таблицы и колонки подставляются в текст запроса, поэтому берутся ТОЛЬКО из белого
	// списка: параметры запроса такое подставить не умеют, а склейка чужой строки — это инъекция.
	if !allowedForCleanup[table+"."+timeColumn] {
		return 0, fmt.Errorf("rollup: чистка %s.%s не разрешена", table, timeColumn)
	}

	query := fmt.Sprintf(`
		DELETE FROM %s WHERE ctid IN (
			SELECT ctid FROM %s WHERE %s < $1 LIMIT $2)`, table, table, timeColumn)

	var total int64
	for {
		tag, err := r.pool.Exec(ctx, query, before, deleteBatch)
		if err != nil {
			return total, fmt.Errorf("cleanup %s: %w", table, err)
		}
		total += tag.RowsAffected()
		if tag.RowsAffected() < deleteBatch {
			return total, nil
		}
		if err := ctx.Err(); err != nil {
			return total, err
		}
	}
}

var allowedForCleanup = map[string]bool{
	"server_observation.observed_at": true,
	"poll_run.finished_at":           true,
	"domain_event.occurred_at":       true,
}
