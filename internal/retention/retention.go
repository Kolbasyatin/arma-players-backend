// Package retention сворачивает историю нагрузки и чистит растущие таблицы.
//
// Зачем он есть. Замер 23.09.2026: за 13 дней работы poll_run, domain_event и server_observation
// заняли 3,1 ГБ из 4,1 и росли на ~240 МБ в сутки. В отличие от прошлого случая с распуханием
// это живые строки, и VACUUM тут не поможет — помогает только перестать хранить всё подряд.
//
// Порядок внутри прохода значим: сначала свёртка, потом чистка. Наоборот — потеряем данные,
// которые ещё не посчитаны.
package retention

import (
	"context"
	"log/slog"
	"time"
)

// Store — то, что нужно от хранилища. Объявлен здесь, у потребителя.
type Store interface {
	RollUpLoad10m(ctx context.Context, now time.Time, overlap time.Duration) (int64, error)
	RollUpLoadDaily(ctx context.Context, now time.Time, overlapDays int) (int64, error)
	RollUpPollRuns(ctx context.Context, now time.Time, overlapDays int) (int64, error)
	DeleteOlderThan(ctx context.Context, table, timeColumn string, before time.Time) (int64, error)
}

// Config — сроки хранения подробностей. Свёртки не удаляются никогда: они на два порядка меньше.
type Config struct {
	// Interval — как часто выполняется проход.
	Interval time.Duration
	// Overlap — насколько назад пересчитывается детальная свёртка. Должен быть заметно больше
	// Interval: так интервал, свёрнутый наполовину из-за рестарта, досчитается на следующем проходе.
	Overlap time.Duration
	// OverlapDays — сколько последних суток пересчитывать в суточных свёртках. Минимум 1:
	// текущие сутки не закончились и обязаны обновляться.
	OverlapDays int

	ObservationRetention time.Duration
	PollRunRetention     time.Duration
	EventRetention       time.Duration
}

type Service struct {
	store Store
	cfg   Config
	log   *slog.Logger
	now   func() time.Time
}

func NewService(store Store, cfg Config, log *slog.Logger) *Service {
	return &Service{store: store, cfg: cfg, log: log, now: time.Now}
}

// Run выполняет один проход: свёртки, затем чистка.
//
// Ошибка свёртки ОТМЕНЯЕТ чистку: удалять подробности, не посчитав их, — единственный способ
// потерять данные безвозвратно. Лучше дать таблицам вырасти ещё на проход.
func (s *Service) Run(ctx context.Context) error {
	now := s.now().UTC()

	buckets, err := s.store.RollUpLoad10m(ctx, now, s.cfg.Overlap)
	if err != nil {
		return err
	}
	days, err := s.store.RollUpLoadDaily(ctx, now, s.cfg.OverlapDays)
	if err != nil {
		return err
	}
	polls, err := s.store.RollUpPollRuns(ctx, now, s.cfg.OverlapDays)
	if err != nil {
		return err
	}

	s.log.Info("retention: rolled up", "buckets_10m", buckets, "days", days, "poll_days", polls)

	cleaned := map[string]int64{}
	for _, target := range []struct {
		table, column string
		retention     time.Duration
	}{
		{"server_observation", "observed_at", s.cfg.ObservationRetention},
		{"poll_run", "finished_at", s.cfg.PollRunRetention},
		{"domain_event", "occurred_at", s.cfg.EventRetention},
	} {
		// Ноль выключает чистку этой таблицы: полезно, когда данные нужны целиком
		// для разбора и место пока есть.
		if target.retention <= 0 {
			continue
		}
		deleted, err := s.store.DeleteOlderThan(ctx, target.table, target.column, now.Add(-target.retention))
		if err != nil {
			return err
		}
		if deleted > 0 {
			cleaned[target.table] = deleted
		}
	}

	if len(cleaned) > 0 {
		s.log.Info("retention: deleted", "rows", cleaned)
	}

	return nil
}

// RunLoop выполняет проход раз в Interval. Первый — сразу на старте: после долгого простоя
// накопившееся нужно свернуть не дожидаясь часа.
func (s *Service) RunLoop(ctx context.Context) error {
	for {
		if err := s.Run(ctx); err != nil {
			// Проход не удался — не роняем сервис: наблюдение важнее уборки.
			s.log.Error("retention: pass failed", "err", err)
		}

		timer := time.NewTimer(s.cfg.Interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}
