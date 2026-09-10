// Package tracking — минутный опрос серверов с tracking_enabled (AGENTS §14):
// найти текущую комнату по hostAddress → listPlayers → отдать наблюдение в presence.
// Каждый запрос фиксируется в poll_run; неудача не меняет состояние игроков.
package tracking

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"armaplayers/internal/bohemia"
	"armaplayers/internal/observation"
	"armaplayers/internal/presence"
)

// Server — отслеживаемый сервер из каталога.
type Server struct {
	ID          int64
	Name        string
	HostAddress string
}

// RoomSource — Bohemia API; реализация *bohemia.Client.
type RoomSource interface {
	SearchRooms(ctx context.Context, accessToken string, q bohemia.RoomSearch) (bohemia.SearchRoomsResponse, error)
	ListPlayers(ctx context.Context, accessToken, roomID string) (bohemia.ListPlayersResponse, error)
}

type TokenProvider interface {
	Get(ctx context.Context) (string, error)
	Invalidate()
}

// Presence — diff engine; реализация *presence.Service.
type Presence interface {
	Apply(ctx context.Context, obs presence.Observation) (presence.Result, error)
}

// Repository — хранилище tracking. Реализация в storage/postgres.
type Repository interface {
	TrackedServers(ctx context.Context) ([]Server, error)
	// SaveRoomObservation обновляет текущие поля сервера, ключи идентичности и пишет снимок TRACKING_POLL.
	SaveRoomObservation(ctx context.Context, serverID int64, room bohemia.Room, observedAt time.Time, raw []byte, rawExpiresAt time.Time) error
	// SaveRawPayload сохраняет сырой ответ listPlayers.
	SaveRawPayload(ctx context.Context, kind string, fetchedAt, expiresAt time.Time, raw []byte) (int64, error)
	RecordPollRun(ctx context.Context, run observation.PollRun) error
}

// Config — параметры цикла.
type Config struct {
	Interval     time.Duration // между обходами; default 1m
	Concurrency  int           // одновременно опрашиваемых серверов; default 4
	RawRetention time.Duration // срок хранения сырых ответов; default 14d
}

func (c Config) withDefaults() Config {
	if c.Interval <= 0 {
		c.Interval = time.Minute
	}
	if c.Concurrency <= 0 {
		c.Concurrency = 4
	}
	if c.RawRetention <= 0 {
		c.RawRetention = 14 * 24 * time.Hour
	}
	return c
}

// Tracker опрашивает отслеживаемые серверы.
type Tracker struct {
	src      RoomSource
	tokens   TokenProvider
	repo     Repository
	presence Presence
	cfg      Config
	log      *slog.Logger
	now      func() time.Time

	mu       sync.Mutex
	replayed map[int64]bool // серверы, у которых уже был успешный listPlayers после старта процесса
}

func NewTracker(src RoomSource, tokens TokenProvider, repo Repository, pres Presence, cfg Config, log *slog.Logger) *Tracker {
	if log == nil {
		log = slog.Default()
	}
	return &Tracker{src: src, tokens: tokens, repo: repo, presence: pres, cfg: cfg.withDefaults(), log: log, now: time.Now, replayed: map[int64]bool{}}
}

// RunLoop — обход сразу и далее каждые Interval, до отмены ctx.
func (t *Tracker) RunLoop(ctx context.Context) error {
	ticker := time.NewTicker(t.cfg.Interval)
	defer ticker.Stop()
	for {
		t.PollAll(ctx)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// PollAll опрашивает все отслеживаемые серверы, не более Concurrency одновременно.
// Ошибки отдельных серверов логируются и учтены в poll_run; обход не прерывают.
func (t *Tracker) PollAll(ctx context.Context) {
	servers, err := t.repo.TrackedServers(ctx)
	if err != nil {
		t.log.Error("tracking: load servers", "err", err)
		return
	}
	if len(servers) == 0 {
		return
	}

	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(t.cfg.Concurrency)
	for _, srv := range servers {
		g.Go(func() error {
			t.pollServer(ctx, srv)
			return nil
		})
	}
	_ = g.Wait()
}

// pollServer — один цикл для одного сервера: RESOLVE_ROOM, затем LIST_PLAYERS.
func (t *Tracker) pollServer(ctx context.Context, srv Server) {
	accessToken, err := t.tokens.Get(ctx)
	if err != nil {
		t.record(ctx, srv.ID, observation.PollResolveRoom, t.now(), "", err, nil)
		return
	}

	// 1. Текущая комната по адресу.
	started := t.now()
	search, err := t.src.SearchRooms(ctx, accessToken, bohemia.RoomSearch{HostAddress: srv.HostAddress, Limit: 5})
	if err != nil {
		t.record(ctx, srv.ID, observation.PollResolveRoom, started, "", err, nil)
		return
	}
	room, found := pickRoom(search.Rooms, srv.HostAddress)
	if !found {
		t.record(ctx, srv.ID, observation.PollResolveRoom, started, "", errRoomNotFound, nil)
		return
	}
	observedAt := t.now()
	if err := t.repo.SaveRoomObservation(ctx, srv.ID, room, observedAt, search.Raw, observedAt.Add(t.cfg.RawRetention)); err != nil {
		t.log.Error("tracking: save room observation", "server_id", srv.ID, "err", err)
	}
	t.record(ctx, srv.ID, observation.PollResolveRoom, started, room.ID, nil, &room)

	// 2. Игроки.
	started = t.now()
	list, err := t.src.ListPlayers(ctx, accessToken, room.ID)
	if err != nil {
		t.record(ctx, srv.ID, observation.PollListPlayers, started, room.ID, err, &room)
		return
	}
	observedAt = t.now()
	if _, err := t.repo.SaveRawPayload(ctx, "LIST_PLAYERS", observedAt, observedAt.Add(t.cfg.RawRetention), list.Raw); err != nil {
		t.log.Error("tracking: save raw listPlayers", "server_id", srv.ID, "err", err)
	}

	res, err := t.presence.Apply(ctx, presence.Observation{
		ServerID: srv.ID, ObservedAt: observedAt,
		Connected: list.ConnectedPlayers, Queue: list.QueuePlayers,
		StartupReplay: !t.wasReplayed(srv.ID),
	})
	if err != nil {
		t.record(ctx, srv.ID, observation.PollListPlayers, started, room.ID, err, &room)
		return
	}
	t.markReplayed(srv.ID)

	connected, queued := len(list.ConnectedPlayers), len(list.QueuePlayers)
	run := t.buildRun(srv.ID, observation.PollListPlayers, started, room.ID, nil, &room)
	run.ConnectedCount, run.QueueCount = &connected, &queued
	if err := t.repo.RecordPollRun(ctx, run); err != nil {
		t.log.Error("tracking: record poll_run", "server_id", srv.ID, "err", err)
	}
	t.log.Info("tracking: polled", "server_id", srv.ID, "server", srv.Name, "players", res.Players, "queued", res.Queued,
		"joined", res.Joined, "left", res.Left, "suspected", res.SuspectedGone, "gap_closed", res.DataGapClosed, "new_players", res.NewPlayers)
}

var errRoomNotFound = errors.New("room not found for hostAddress")

// pickRoom — комната с точным совпадением адреса; при нескольких — с самым свежим heartbeat.
func pickRoom(rooms []bohemia.Room, hostAddress string) (bohemia.Room, bool) {
	var best bohemia.Room
	found := false
	for _, r := range rooms {
		if r.HostAddress != hostAddress {
			continue
		}
		if !found || r.Updated > best.Updated {
			best, found = r, true
		}
	}
	return best, found
}

func (t *Tracker) buildRun(serverID int64, typ observation.PollType, started time.Time, roomID string, err error, room *bohemia.Room) observation.PollRun {
	status, httpStatus := observation.StatusFromError(err)
	if errors.Is(err, errRoomNotFound) {
		status = observation.StatusRoomNotFound
	}
	run := observation.PollRun{
		ServerID: &serverID, Type: typ, StartedAt: started, FinishedAt: t.now(),
		Status: status, HTTPStatus: httpStatus, RoomID: roomID,
	}
	if err != nil {
		run.ErrorMessage = err.Error()
	}
	if room != nil && room.Updated != 0 {
		u := time.Unix(room.Updated, 0).UTC()
		run.DataUpdatedAt = &u
	}
	return run
}

// record пишет poll_run и реагирует на вид ошибки (AUTH_ERROR → сброс токена).
func (t *Tracker) record(ctx context.Context, serverID int64, typ observation.PollType, started time.Time, roomID string, err error, room *bohemia.Room) {
	run := t.buildRun(serverID, typ, started, roomID, err, room)
	if run.Status == observation.StatusAuthError {
		t.tokens.Invalidate()
	}
	if err != nil {
		t.log.Warn("tracking: poll failed", "server_id", serverID, "type", typ, "status", run.Status, "err", err)
	}
	if rerr := t.repo.RecordPollRun(ctx, run); rerr != nil {
		t.log.Error("tracking: record poll_run", "server_id", serverID, "err", fmt.Errorf("%w (original: %v)", rerr, err))
	}
}

func (t *Tracker) wasReplayed(serverID int64) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.replayed[serverID]
}

func (t *Tracker) markReplayed(serverID int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.replayed[serverID] = true
}
