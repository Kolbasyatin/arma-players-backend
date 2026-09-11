package catalog

import (
	"context"
	"time"
)

// RunLoop держит расписание полного скана: если с последнего успешного прошло больше interval —
// сканирует сразу, иначе ждёт до срока. После ошибки повторяет через retry. Возвращается только
// по отмене ctx — это способ остановить цикл при shutdown.
func (s *Scanner) RunLoop(ctx context.Context, interval, retry time.Duration) error {
	for {
		wait, err := s.untilNextScan(ctx, interval)
		if err != nil {
			s.log.Error("lobby scan: read last scan time", "err", err)
			wait = retry
		}

		if wait > 0 {
			s.log.Info("lobby scan: next run scheduled", "in", wait.Round(time.Second).String())
			if err := sleep(ctx, wait); err != nil {
				return err // ctx отменён — штатный выход
			}
		}

		res, err := s.RunLobbyScan(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			s.log.Error("lobby scan failed", "err", err, "retry_in", retry.String())
			if err := sleep(ctx, retry); err != nil {
				return err
			}
			continue
		}
		s.log.Info("lobby scan finished", "pages", res.Pages, "rooms", res.Rooms,
			"new_servers", res.ServersCreated, "deactivated", res.Deactivated, "merged", res.Merged,
			"tracking_manual", res.Tracking.Manual, "tracking_auto", res.Tracking.Auto, "tracking_disabled", res.Tracking.Disabled,
			"duration", res.Duration.Round(time.Millisecond).String())
	}
}

// untilNextScan — сколько ждать до следующего скана; 0, если сканов не было или срок прошёл.
func (s *Scanner) untilNextScan(ctx context.Context, interval time.Duration) (time.Duration, error) {
	last, ok, err := s.repo.LastSuccessfulScanAt(ctx)
	if err != nil || !ok {
		return 0, err
	}
	wait := last.Add(interval).Sub(s.now())
	if wait < 0 {
		wait = 0
	}
	return wait, nil
}

// sleep ждёт d или отмены ctx — что наступит раньше. time.Sleep так не умеет.
func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
