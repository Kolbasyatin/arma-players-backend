package retention_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"armaplayers/internal/retention"
)

type fakeStore struct {
	rollupErr error
	calls     []string
	deleted   map[string]time.Time
}

func (f *fakeStore) RollUpLoad10m(context.Context, time.Time, time.Duration) (int64, error) {
	f.calls = append(f.calls, "rollup10m")
	return 0, f.rollupErr
}

func (f *fakeStore) RollUpLoadDaily(context.Context, time.Time, int) (int64, error) {
	f.calls = append(f.calls, "rollupDaily")
	return 0, nil
}

func (f *fakeStore) RollUpPollRuns(context.Context, time.Time, int) (int64, error) {
	f.calls = append(f.calls, "rollupPolls")
	return 0, nil
}

func (f *fakeStore) DeleteOlderThan(_ context.Context, table, _ string, before time.Time) (int64, error) {
	f.calls = append(f.calls, "delete:"+table)
	if f.deleted == nil {
		f.deleted = map[string]time.Time{}
	}
	f.deleted[table] = before
	return 0, nil
}

func newService(store *fakeStore, cfg retention.Config) *retention.Service {
	return retention.NewService(store, cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func config() retention.Config {
	return retention.Config{
		Interval: time.Hour, Overlap: 3 * time.Hour, OverlapDays: 2,
		ObservationRetention: 168 * time.Hour,
		PollRunRetention:     168 * time.Hour,
		EventRetention:       168 * time.Hour,
	}
}

func TestRun_rollsUpBeforeDeleting(t *testing.T) {
	store := &fakeStore{}

	if err := newService(store, config()).Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Порядок здесь не косметика: удалить подробности, не посчитав их, — единственный
	// способ потерять данные безвозвратно.
	want := []string{"rollup10m", "rollupDaily", "rollupPolls",
		"delete:server_observation", "delete:poll_run", "delete:domain_event"}
	if len(store.calls) != len(want) {
		t.Fatalf("вызовы: want %v, got %v", want, store.calls)
	}
	for i := range want {
		if store.calls[i] != want[i] {
			t.Errorf("вызов %d: want %s, got %s", i, want[i], store.calls[i])
		}
	}
}

func TestRun_failedRollupCancelsCleanup(t *testing.T) {
	store := &fakeStore{rollupErr: errors.New("база недоступна")}

	err := newService(store, config()).Run(context.Background())

	if err == nil {
		t.Fatal("ошибка свёртки должна возвращаться наверх")
	}
	for _, call := range store.calls {
		if len(call) > 7 && call[:7] == "delete:" {
			t.Errorf("после неудачной свёртки чистки быть не должно, был %s", call)
		}
	}
}

func TestRun_zeroRetentionDisablesCleanupOfThatTable(t *testing.T) {
	store := &fakeStore{}
	cfg := config()
	// Ноль полезен, когда данные нужны целиком для разбора, а место пока есть.
	cfg.EventRetention = 0

	if err := newService(store, cfg).Run(context.Background()); err != nil {
		t.Fatal(err)
	}

	if _, cleaned := store.deleted["domain_event"]; cleaned {
		t.Error("нулевой срок хранения обязан выключать чистку таблицы")
	}
	if _, cleaned := store.deleted["poll_run"]; !cleaned {
		t.Error("остальные таблицы чиститься должны")
	}
}

func TestRun_cutoffIsRetentionBeforeNow(t *testing.T) {
	store := &fakeStore{}
	cfg := config()
	cfg.ObservationRetention = 48 * time.Hour

	before := time.Now().UTC()
	if err := newService(store, cfg).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	after := time.Now().UTC()

	// Граница обязана лежать ровно на «срок хранения назад» от момента прохода, а момент
	// взят где-то между before и after.
	cutoff := store.deleted["server_observation"]
	if cutoff.Before(before.Add(-48*time.Hour)) || cutoff.After(after.Add(-48*time.Hour)) {
		t.Errorf("граница чистки: got %s, ожидали между %s и %s",
			cutoff, before.Add(-48*time.Hour), after.Add(-48*time.Hour))
	}
}
