// Package catalog — каталог серверов (ADR 0004): суточный полный скан лобби, upsert серверов
// по hostAddress, история внешних ключей, снимки состояния. Игроков здесь нет.
package catalog

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"armaplayers/internal/bohemia"
	"armaplayers/internal/observation"
)

// Source — откуда берутся комнаты. Реализация — *bohemia.Client; интерфейс объявлен здесь,
// чтобы скан тестировался на заранее заданных страницах без HTTP.
type Source interface {
	SearchAllRooms(ctx context.Context, accessToken string, pageSize int, visit func(page bohemia.SearchRoomsResponse) error) error
}

// TokenProvider — откуда берётся access token.
type TokenProvider interface {
	Get(ctx context.Context) (string, error)
	Invalidate()
}

// PageStats — что произошло при сохранении одной страницы.
type PageStats struct {
	Rooms          int
	ServersCreated int
}

// TrackingRules — правила выбора серверов для минутного опроса (ADR 0008).
type TrackingRules struct {
	ManualHosts    []string // всегда отслеживаются
	AutoMinPlayers int      // 0 — авто-правило выключено
	AutoMaxServers int
	AutoLookback   time.Duration // окно, за которое берётся пиковый онлайн; default 7d
}

// TrackingStats — итог применения правил.
type TrackingStats struct {
	Manual, Auto, Disabled int64
}

// Repository — хранилище каталога. Реализация в storage/postgres.
type Repository interface {
	// SaveLobbyPage атомарно сохраняет страницу скана: raw payload, upsert серверов по hostAddress,
	// ключи идентичности, снимок состояния каждой комнаты.
	SaveLobbyPage(ctx context.Context, observedAt time.Time, rooms []bohemia.Room, raw []byte, rawExpiresAt time.Time) (PageStats, error)
	// DeactivateUnseen помечает active=false серверы, не встреченные с начала скана. Возвращает их число.
	DeactivateUnseen(ctx context.Context, scanStartedAt time.Time) (int64, error)
	// RecordPollRun пишет строку журнала poll_run.
	RecordPollRun(ctx context.Context, run observation.PollRun) error
	// LastSuccessfulScanAt — когда закончился последний успешный полный скан; ok=false, если сканов не было.
	LastSuccessfulScanAt(ctx context.Context) (at time.Time, ok bool, err error)
	// ApplyTrackingRules пересчитывает tracking_enabled/tracking_source по правилам.
	ApplyTrackingRules(ctx context.Context, rules TrackingRules) (TrackingStats, error)
	// DeleteExpiredRawPayloads удаляет сырые ответы с истёкшим сроком. Возвращает число удалённых.
	DeleteExpiredRawPayloads(ctx context.Context, now time.Time) (int64, error)
}

// Scanner выполняет полный скан лобби.
type Scanner struct {
	src          Source
	tokens       TokenProvider
	repo         Repository
	pageSize     int
	rawRetention time.Duration
	rules        TrackingRules
	log          *slog.Logger
	now          func() time.Time
}

func NewScanner(src Source, tokens TokenProvider, repo Repository, pageSize int, rawRetention time.Duration, rules TrackingRules, log *slog.Logger) *Scanner {
	if log == nil {
		log = slog.Default()
	}
	return &Scanner{src: src, tokens: tokens, repo: repo, pageSize: pageSize, rawRetention: rawRetention, rules: rules, log: log, now: time.Now}
}

// ScanResult — итог полного скана.
type ScanResult struct {
	Pages          int
	Rooms          int
	ServersCreated int
	Deactivated    int64
	Tracking       TrackingStats
	Duration       time.Duration
}

// RunLobbyScan обходит всё лобби, сохраняет каждую страницу отдельной транзакцией и в конце
// помечает исчезнувшие серверы. Любая ошибка фиксируется в poll_run и возвращается;
// уже сохранённые страницы остаются — следующий скан их обновит.
func (s *Scanner) RunLobbyScan(ctx context.Context) (ScanResult, error) {
	started := s.now()
	var res ScanResult

	err := s.scan(ctx, started, &res)

	status, httpStatus := observation.StatusFromError(err)
	run := observation.PollRun{
		Type:       observation.PollLobbyScan,
		StartedAt:  started,
		FinishedAt: s.now(),
		Status:     status,
		HTTPStatus: httpStatus,
	}
	if err != nil {
		run.ErrorMessage = err.Error()
		if status == observation.StatusAuthError {
			s.tokens.Invalidate()
		}
	}
	if rerr := s.repo.RecordPollRun(ctx, run); rerr != nil {
		s.log.Error("lobby scan: record poll_run", "err", rerr)
	}

	res.Duration = run.FinishedAt.Sub(started)
	return res, err
}

func (s *Scanner) scan(ctx context.Context, started time.Time, res *ScanResult) error {
	accessToken, err := s.tokens.Get(ctx)
	if err != nil {
		return fmt.Errorf("lobby scan: token: %w", err)
	}

	err = s.src.SearchAllRooms(ctx, accessToken, s.pageSize, func(page bohemia.SearchRoomsResponse) error {
		observedAt := s.now()
		stats, err := s.repo.SaveLobbyPage(ctx, observedAt, page.Rooms, page.Raw, observedAt.Add(s.rawRetention))
		if err != nil {
			return fmt.Errorf("lobby scan: save page from=%d: %w", page.SearchFrom, err)
		}
		res.Pages++
		res.Rooms += stats.Rooms
		res.ServersCreated += stats.ServersCreated
		s.log.Info("lobby scan: page saved", "from", page.SearchFrom, "rooms", stats.Rooms, "new_servers", stats.ServersCreated, "total_count", page.TotalCount)
		return nil
	})
	if err != nil {
		return err
	}

	res.Deactivated, err = s.repo.DeactivateUnseen(ctx, started)
	if err != nil {
		return fmt.Errorf("lobby scan: deactivate unseen: %w", err)
	}
	res.Tracking, err = s.repo.ApplyTrackingRules(ctx, s.rules)
	if err != nil {
		return fmt.Errorf("lobby scan: tracking rules: %w", err)
	}
	return nil
}

// ApplyTrackingRules пересчитывает выбор серверов без скана — при старте процесса,
// чтобы изменённый TRACK_* в конфиге не ждал следующего суточного скана.
func (s *Scanner) ApplyTrackingRules(ctx context.Context) (TrackingStats, error) {
	return s.repo.ApplyTrackingRules(ctx, s.rules)
}

// RunRetentionLoop раз в interval удаляет сырые ответы с истёкшим сроком (AGENTS §12).
func (s *Scanner) RunRetentionLoop(ctx context.Context, interval time.Duration) error {
	for {
		n, err := s.repo.DeleteExpiredRawPayloads(ctx, s.now())
		if err != nil {
			s.log.Error("retention: delete expired raw payloads", "err", err)
		} else if n > 0 {
			s.log.Info("retention: raw payloads deleted", "count", n)
		}
		if err := sleep(ctx, interval); err != nil {
			return err
		}
	}
}
