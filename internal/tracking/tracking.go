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

	"armaplayers/internal/bohemia"
	"armaplayers/internal/observation"
	"armaplayers/internal/presence"
)

// Server — отслеживаемый сервер из каталога.
type Server struct {
	ID            int64
	Name          string
	HostAddress   string
	CurrentRoomID string // roomId с прошлого наблюдения; запасной путь, если по адресу комнату не нашли
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

// RunLoop — обход сразу и далее каждые Interval, до отмены ctx. Сам обход занимает ~90% интервала
// (см. PollAll), поэтому нагрузка на Bohemia ровная: 2 запроса на сервер, растянутые по времени.
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

// PollAll опрашивает все отслеживаемые серверы, равномерно распределяя старты по интервалу:
// N серверов за Interval → один старт каждые Interval/N, а не залп из N запросов в начале минуты.
// Concurrency — страховка: если ответы медленнее шага, одновременно работает не больше N опросов.
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

	// Растягиваем на 90% интервала, чтобы последний опрос успел закончиться до следующего обхода.
	spacing := t.cfg.Interval * 9 / 10 / time.Duration(len(servers))

	var wg sync.WaitGroup
	sem := make(chan struct{}, t.cfg.Concurrency)
	for i, srv := range servers {
		if i > 0 {
			if err := sleepCtx(ctx, spacing); err != nil {
				break // shutdown: не стартуем оставшиеся
			}
		}
		sem <- struct{}{} // занять слот; блокируется, если все Concurrency заняты
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { <-sem }() // освободить слот
			t.pollServer(ctx, srv)
		}()
	}
	wg.Wait()
}

// sleepCtx ждёт d или отмены ctx.
func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
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
	roomID := room.ID
	if found {
		observedAt := t.now()
		if err := t.repo.SaveRoomObservation(ctx, srv.ID, room, observedAt, search.Raw, observedAt.Add(t.cfg.RawRetention)); err != nil {
			t.log.Error("tracking: save room observation", "server_id", srv.ID, "err", err)
		}
		t.record(ctx, srv.ID, observation.PollResolveRoom, started, room.ID, nil, &room)
	} else {
		// Сервер не найден по адресу — переехал на другой порт/IP или перезапускается. roomId при переезде
		// сохраняется (наблюдение 2026-09-11), поэтому пробуем listPlayers по прошлому roomId: сессии игроков
		// не рвутся, а адрес обновит следующий скан лобби через резолюцию по ROOM_ID.
		t.record(ctx, srv.ID, observation.PollResolveRoom, started, "", errRoomNotFound, nil)
		if srv.CurrentRoomID == "" {
			return
		}
		roomID = srv.CurrentRoomID
	}

	// 2. Игроки.
	var roomPtr *bohemia.Room
	if found {
		roomPtr = &room
	}
	started = t.now()
	list, err := t.src.ListPlayers(ctx, accessToken, roomID)
	if err != nil {
		t.record(ctx, srv.ID, observation.PollListPlayers, started, roomID, err, roomPtr)
		return
	}
	observedAt := t.now()
	if _, err := t.repo.SaveRawPayload(ctx, "LIST_PLAYERS", observedAt, observedAt.Add(t.cfg.RawRetention), list.Raw); err != nil {
		t.log.Error("tracking: save raw listPlayers", "server_id", srv.ID, "err", err)
	}

	res, err := t.presence.Apply(ctx, presence.Observation{
		ServerID: srv.ID, ObservedAt: observedAt,
		Connected: list.ConnectedPlayers, Queue: list.QueuePlayers,
		StartupReplay: !t.wasReplayed(srv.ID),
	})
	if err != nil {
		t.record(ctx, srv.ID, observation.PollListPlayers, started, roomID, err, roomPtr)
		return
	}
	t.markReplayed(srv.ID)

	connected, queued := len(list.ConnectedPlayers), len(list.QueuePlayers)
	run := t.buildRun(srv.ID, observation.PollListPlayers, started, roomID, nil, roomPtr)
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
