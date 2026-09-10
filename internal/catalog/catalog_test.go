package catalog_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"armaplayers/internal/bohemia"
	"armaplayers/internal/catalog"
	"armaplayers/internal/observation"
	"armaplayers/internal/token"
)

type fakeSource struct {
	pages []bohemia.SearchRoomsResponse
	err   error
}

func (f *fakeSource) SearchAllRooms(_ context.Context, _ string, _ int, visit func(bohemia.SearchRoomsResponse) error) error {
	for _, p := range f.pages {
		if err := visit(p); err != nil {
			return err
		}
	}
	return f.err
}

type fakeTokens struct {
	err         error
	invalidated int
}

func (f *fakeTokens) Get(context.Context) (string, error) { return "tok", f.err }
func (f *fakeTokens) Invalidate()                         { f.invalidated++ }

// fakeRepo — репозиторий в памяти. В тестах RunLoop его зовёт горутина цикла, а читает тест,
// поэтому все поля под мьютексом (иначе go test -race покажет гонку).
type fakeRepo struct {
	mu          sync.Mutex
	saved       [][]bohemia.Room
	deactivated int64
	runs        []observation.PollRun
	lastScan    time.Time // нулевое значение = сканов не было
}

func (f *fakeRepo) SaveLobbyPage(_ context.Context, _ time.Time, rooms []bohemia.Room, _ []byte, _ time.Time) (catalog.PageStats, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saved = append(f.saved, rooms)
	return catalog.PageStats{Rooms: len(rooms), ServersCreated: len(rooms)}, nil
}

func (f *fakeRepo) DeactivateUnseen(context.Context, time.Time) (int64, error) {
	return f.deactivated, nil
}

func (f *fakeRepo) RecordPollRun(_ context.Context, run observation.PollRun) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, run)
	if run.Status == observation.StatusSuccess {
		f.lastScan = run.FinishedAt
	}
	return nil
}

func (f *fakeRepo) LastSuccessfulScanAt(context.Context) (time.Time, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.lastScan, !f.lastScan.IsZero(), nil
}

func (f *fakeRepo) ApplyTrackingRules(context.Context, catalog.TrackingRules) (catalog.TrackingStats, error) {
	return catalog.TrackingStats{}, nil
}

func (f *fakeRepo) DeleteExpiredRawPayloads(context.Context, time.Time) (int64, error) { return 0, nil }

func (f *fakeRepo) savedPages() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.saved)
}

func rooms(ids ...string) []bohemia.Room {
	out := make([]bohemia.Room, 0, len(ids))
	for _, id := range ids {
		out = append(out, bohemia.Room{ID: id, HostAddress: id + ":2001"})
	}
	return out
}

func TestScanner_success(t *testing.T) {
	src := &fakeSource{pages: []bohemia.SearchRoomsResponse{{Rooms: rooms("a", "b")}, {Rooms: rooms("c")}}}
	repo := &fakeRepo{deactivated: 4}
	s := catalog.NewScanner(src, &fakeTokens{}, repo, 2, time.Hour, catalog.TrackingRules{}, nil)

	res, err := s.RunLobbyScan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if res.Pages != 2 || res.Rooms != 3 || res.ServersCreated != 3 || res.Deactivated != 4 {
		t.Errorf("result: %+v", res)
	}
	if len(repo.runs) != 1 || repo.runs[0].Status != observation.StatusSuccess || repo.runs[0].Type != observation.PollLobbyScan {
		t.Errorf("poll_run: %+v", repo.runs)
	}
}

func TestScanner_authErrorInvalidatesToken(t *testing.T) {
	src := &fakeSource{err: &bohemia.Error{Kind: bohemia.KindAuthError, Op: "searchRooms", HTTPStatus: 401}}
	tokens := &fakeTokens{}
	repo := &fakeRepo{}
	s := catalog.NewScanner(src, tokens, repo, 2, time.Hour, catalog.TrackingRules{}, nil)

	_, err := s.RunLobbyScan(context.Background())
	if err == nil {
		t.Fatal("want error")
	}
	if tokens.invalidated != 1 {
		t.Errorf("token must be invalidated on AUTH_ERROR, got %d", tokens.invalidated)
	}
	if len(repo.runs) != 1 || repo.runs[0].Status != observation.StatusAuthError || repo.runs[0].HTTPStatus != 401 {
		t.Errorf("poll_run: %+v", repo.runs)
	}
}

func TestScanner_tokenUnavailable(t *testing.T) {
	repo := &fakeRepo{}
	s := catalog.NewScanner(&fakeSource{}, &fakeTokens{err: token.ErrNotAvailable}, repo, 2, time.Hour, catalog.TrackingRules{}, nil)

	_, err := s.RunLobbyScan(context.Background())
	if !errors.Is(err, token.ErrNotAvailable) {
		t.Fatalf("want ErrNotAvailable, got %v", err)
	}
	if len(repo.runs) != 1 || repo.runs[0].Status != observation.StatusTokenUnavailable {
		t.Errorf("poll_run: %+v", repo.runs)
	}
}

func TestScanner_RunLoop_scansImmediatelyWhenNeverScanned(t *testing.T) {
	src := &fakeSource{pages: []bohemia.SearchRoomsResponse{{Rooms: rooms("a")}}}
	repo := &fakeRepo{}
	s := catalog.NewScanner(src, &fakeTokens{}, repo, 2, time.Hour, catalog.TrackingRules{}, nil)

	// Интервал сутки: после первого скана цикл уснёт до следующего — отменяем ctx и ждём выхода.
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.RunLoop(ctx, 24*time.Hour, time.Minute) }()

	waitFor(t, func() bool { return repo.savedPages() == 1 })
	cancel()

	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("RunLoop must return ctx error on cancel, got %v", err)
	}
}

func TestScanner_RunLoop_waitsWhenRecentlyScanned(t *testing.T) {
	repo := &fakeRepo{lastScan: time.Now()}
	s := catalog.NewScanner(&fakeSource{}, &fakeTokens{}, repo, 2, time.Hour, catalog.TrackingRules{}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := s.RunLoop(ctx, 24*time.Hour, time.Minute)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want DeadlineExceeded, got %v", err)
	}
	if n := repo.savedPages(); n != 0 {
		t.Errorf("must not scan: last scan was just now, saved %d pages", n)
	}
}

// waitFor опрашивает условие до 2 секунд; для проверки эффекта работы другой горутины.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}
