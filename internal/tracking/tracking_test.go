package tracking_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"armaplayers/internal/bohemia"
	"armaplayers/internal/observation"
	"armaplayers/internal/presence"
	"armaplayers/internal/tracking"
)

type fakeSrc struct {
	rooms     []bohemia.Room
	searchErr error
	players   bohemia.ListPlayersResponse
	listErr   error
}

func (f *fakeSrc) SearchRooms(context.Context, string, bohemia.RoomSearch) (bohemia.SearchRoomsResponse, error) {
	return bohemia.SearchRoomsResponse{Rooms: f.rooms, Raw: []byte(`{}`)}, f.searchErr
}
func (f *fakeSrc) ListPlayers(context.Context, string, string) (bohemia.ListPlayersResponse, error) {
	return f.players, f.listErr
}

type fakeTokens struct{ invalidated int }

func (f *fakeTokens) Get(context.Context) (string, error) { return "tok", nil }
func (f *fakeTokens) Invalidate()                         { f.invalidated++ }

type fakeRepo struct {
	mu      sync.Mutex
	servers []tracking.Server
	runs    []observation.PollRun
	rooms   []bohemia.Room
	raws    int
}

func (f *fakeRepo) TrackedServers(context.Context) ([]tracking.Server, error) { return f.servers, nil }
func (f *fakeRepo) SaveRoomObservation(_ context.Context, _ int64, room bohemia.Room, _ time.Time, _ []byte, _ time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rooms = append(f.rooms, room)
	return nil
}
func (f *fakeRepo) SaveRawPayload(context.Context, string, time.Time, time.Time, []byte) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.raws++
	return 1, nil
}
func (f *fakeRepo) RecordPollRun(_ context.Context, run observation.PollRun) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.runs = append(f.runs, run)
	return nil
}

type fakePresence struct {
	mu  sync.Mutex
	obs []presence.Observation
}

func (f *fakePresence) Apply(_ context.Context, o presence.Observation) (presence.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.obs = append(f.obs, o)
	return presence.Result{Players: len(o.Connected)}, nil
}

func srv() tracking.Server { return tracking.Server{ID: 1, Name: "test", HostAddress: "10.0.0.1:2001"} }

func TestTracker_successfulPoll(t *testing.T) {
	src := &fakeSrc{
		rooms: []bohemia.Room{
			{ID: "old", HostAddress: "10.0.0.1:2001", Updated: 100},
			{ID: "new", HostAddress: "10.0.0.1:2001", Updated: 200},
			{ID: "other", HostAddress: "10.0.0.9:2001", Updated: 999},
		},
		players: bohemia.ListPlayersResponse{ConnectedPlayers: []bohemia.Player{{UserID: "a"}}, QueuePlayers: []bohemia.Player{{UserID: "q"}}},
	}
	repo := &fakeRepo{servers: []tracking.Server{srv()}}
	pres := &fakePresence{}
	tr := tracking.NewTracker(src, &fakeTokens{}, repo, pres, tracking.Config{}, nil)

	tr.PollAll(context.Background())
	tr.PollAll(context.Background())

	if len(repo.runs) != 4 {
		t.Fatalf("poll runs: want 4 (2 polls × RESOLVE+LIST), got %d", len(repo.runs))
	}
	if repo.runs[0].Type != observation.PollResolveRoom || repo.runs[0].Status != observation.StatusSuccess || repo.runs[0].RoomID != "new" {
		t.Errorf("resolve run: %+v", repo.runs[0])
	}
	list := repo.runs[1]
	if list.Type != observation.PollListPlayers || list.Status != observation.StatusSuccess || *list.ConnectedCount != 1 || *list.QueueCount != 1 || list.DataUpdatedAt == nil {
		t.Errorf("list run: %+v", list)
	}
	if len(pres.obs) != 2 || !pres.obs[0].StartupReplay || pres.obs[1].StartupReplay {
		t.Errorf("startup replay only on first successful poll: %+v", pres.obs)
	}
	if len(repo.rooms) != 2 || repo.rooms[0].ID != "new" {
		t.Errorf("room observation must pick freshest exact match: %+v", repo.rooms)
	}
}

func TestTracker_roomNotFound_noKnownRoom(t *testing.T) {
	src := &fakeSrc{rooms: []bohemia.Room{{ID: "x", HostAddress: "10.0.0.9:2001"}}}
	repo := &fakeRepo{servers: []tracking.Server{srv()}}
	pres := &fakePresence{}
	tracking.NewTracker(src, &fakeTokens{}, repo, pres, tracking.Config{}, nil).PollAll(context.Background())

	if len(repo.runs) != 1 || repo.runs[0].Status != observation.StatusRoomNotFound {
		t.Fatalf("runs: %+v", repo.runs)
	}
	if len(pres.obs) != 0 {
		t.Error("presence must not be touched when room not found")
	}
}

// Сервер переехал: по адресу не найден, но прошлый roomId жив → игроки берутся по нему, сессии не рвутся.
func TestTracker_roomNotFound_fallsBackToKnownRoomID(t *testing.T) {
	src := &fakeSrc{
		rooms:   nil,
		players: bohemia.ListPlayersResponse{ConnectedPlayers: []bohemia.Player{{UserID: "a"}}},
	}
	s := srv()
	s.CurrentRoomID = "room-from-yesterday"
	repo := &fakeRepo{servers: []tracking.Server{s}}
	pres := &fakePresence{}
	tracking.NewTracker(src, &fakeTokens{}, repo, pres, tracking.Config{}, nil).PollAll(context.Background())

	if len(repo.runs) != 2 || repo.runs[0].Status != observation.StatusRoomNotFound || repo.runs[1].Status != observation.StatusSuccess || repo.runs[1].RoomID != "room-from-yesterday" {
		t.Fatalf("runs: %+v", repo.runs)
	}
	if len(pres.obs) != 1 || len(pres.obs[0].Connected) != 1 {
		t.Errorf("presence must receive players via fallback roomId: %+v", pres.obs)
	}
	if len(repo.rooms) != 0 {
		t.Error("no room observation without search result")
	}
}

// Прошлый roomId тоже мёртв (рестарт с новым roomId) → фиксируем ROOM_NOT_FOUND от listPlayers, presence не трогаем.
func TestTracker_roomNotFound_staleRoomIDGone(t *testing.T) {
	src := &fakeSrc{listErr: &bohemia.Error{Kind: bohemia.KindRoomNotFound, Op: "listPlayers", HTTPStatus: 404}}
	s := srv()
	s.CurrentRoomID = "stale"
	repo := &fakeRepo{servers: []tracking.Server{s}}
	pres := &fakePresence{}
	tracking.NewTracker(src, &fakeTokens{}, repo, pres, tracking.Config{}, nil).PollAll(context.Background())

	if len(repo.runs) != 2 || repo.runs[1].Status != observation.StatusRoomNotFound {
		t.Fatalf("runs: %+v", repo.runs)
	}
	if len(pres.obs) != 0 {
		t.Error("presence must not be touched")
	}
}

func TestTracker_authErrorInvalidatesToken(t *testing.T) {
	src := &fakeSrc{searchErr: &bohemia.Error{Kind: bohemia.KindAuthError, Op: "searchRooms", HTTPStatus: 403}}
	tokens := &fakeTokens{}
	repo := &fakeRepo{servers: []tracking.Server{srv()}}
	tracking.NewTracker(src, tokens, repo, &fakePresence{}, tracking.Config{}, nil).PollAll(context.Background())

	if tokens.invalidated != 1 {
		t.Errorf("token invalidations: want 1, got %d", tokens.invalidated)
	}
	if len(repo.runs) != 1 || repo.runs[0].Status != observation.StatusAuthError || repo.runs[0].HTTPStatus != 403 {
		t.Errorf("runs: %+v", repo.runs)
	}
}

func TestTracker_listPlayersFailureKeepsPresenceUntouched(t *testing.T) {
	src := &fakeSrc{
		rooms:   []bohemia.Room{{ID: "r", HostAddress: "10.0.0.1:2001"}},
		listErr: &bohemia.Error{Kind: bohemia.KindConnectionTimeout, Op: "listPlayers"},
	}
	repo := &fakeRepo{servers: []tracking.Server{srv()}}
	pres := &fakePresence{}
	tracking.NewTracker(src, &fakeTokens{}, repo, pres, tracking.Config{}, nil).PollAll(context.Background())

	if len(pres.obs) != 0 {
		t.Error("failed poll must not reach presence")
	}
	if len(repo.runs) != 2 || repo.runs[1].Status != observation.StatusConnectionTimeout {
		t.Errorf("runs: %+v", repo.runs)
	}
}

func TestTracker_spreadsPollsOverInterval(t *testing.T) {
	src := &fakeSrc{rooms: []bohemia.Room{
		{ID: "r1", HostAddress: "10.0.0.1:2001"}, {ID: "r2", HostAddress: "10.0.0.2:2001"}, {ID: "r3", HostAddress: "10.0.0.3:2001"},
	}}
	repo := &fakeRepo{servers: []tracking.Server{
		{ID: 1, HostAddress: "10.0.0.1:2001"}, {ID: 2, HostAddress: "10.0.0.2:2001"}, {ID: 3, HostAddress: "10.0.0.3:2001"},
	}}
	// Интервал 300ms, 3 сервера → старты через ~90ms; весь обход ≈ 180ms + время опросов.
	tr := tracking.NewTracker(src, &fakeTokens{}, repo, &fakePresence{}, tracking.Config{Interval: 300 * time.Millisecond}, nil)

	start := time.Now()
	tr.PollAll(context.Background())
	took := time.Since(start)

	if took < 150*time.Millisecond {
		t.Errorf("polls must be spread over the interval, whole pass took only %s", took)
	}
	// Верхняя граница мягкая: CI-раннеры медленные, важен сам факт распределения, а не точность.
	if took > 2*300*time.Millisecond {
		t.Errorf("pass took suspiciously long: %s", took)
	}
	// Старты RESOLVE_ROOM идут по возрастанию времени с заметным шагом.
	var resolves []time.Time
	for _, r := range repo.runs {
		if r.Type == observation.PollResolveRoom {
			resolves = append(resolves, r.StartedAt)
		}
	}
	if len(resolves) != 3 || resolves[1].Sub(resolves[0]) < 50*time.Millisecond || resolves[2].Sub(resolves[1]) < 50*time.Millisecond {
		t.Errorf("resolve start times not spread: %v", resolves)
	}
}
