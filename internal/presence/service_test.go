package presence_test

import (
	"context"
	"testing"
	"time"

	"armaplayers/internal/bohemia"
	"armaplayers/internal/presence"
)

// memStore — хранилище в памяти, реализует Store и Tx (без реальных транзакций).
type memStore struct {
	players  map[string]*memPlayer // bohemia userId → игрок
	nextID   int64
	presence []presence.Session
	queue    []presence.Session
	events   []presence.Event
	lastPoll map[int64]time.Time
}

type memPlayer struct {
	id       int64
	nickname string
}

func newMem() *memStore {
	return &memStore{players: map[string]*memPlayer{}, lastPoll: map[int64]time.Time{}}
}

func (m *memStore) WithTx(ctx context.Context, fn func(tx presence.Tx) error) error { return fn(m) }

func (m *memStore) UpsertPlayer(_ context.Context, p bohemia.Player, _ time.Time) (presence.PlayerRef, error) {
	if pl, ok := m.players[p.UserID]; ok {
		prev := pl.nickname
		pl.nickname = p.Username
		return presence.PlayerRef{PlayerID: pl.id, PreviousNickname: prev}, nil
	}
	m.nextID++
	m.players[p.UserID] = &memPlayer{id: m.nextID, nickname: p.Username}
	return presence.PlayerRef{PlayerID: m.nextID, IsNew: true}, nil
}

func (m *memStore) LastSuccessfulListPlayersAt(_ context.Context, serverID int64) (time.Time, bool, error) {
	t, ok := m.lastPoll[serverID]
	return t, ok, nil
}

func openOf(list []presence.Session, serverID int64) []presence.Session {
	var out []presence.Session
	for _, s := range list {
		if s.ServerID == serverID && s.EndedAt == nil {
			out = append(out, s)
		}
	}
	return out
}

func (m *memStore) OpenPresenceSessions(_ context.Context, serverID int64) ([]presence.Session, error) {
	return openOf(m.presence, serverID), nil
}
func (m *memStore) InsertPresenceSession(_ context.Context, s presence.Session) (int64, error) {
	m.nextID++
	s.ID = m.nextID
	m.presence = append(m.presence, s)
	return s.ID, nil
}
func (m *memStore) UpdatePresenceSession(_ context.Context, s presence.Session) error {
	for i := range m.presence {
		if m.presence[i].ID == s.ID {
			m.presence[i] = s
		}
	}
	return nil
}
func (m *memStore) OpenQueueSessions(_ context.Context, serverID int64) ([]presence.Session, error) {
	return openOf(m.queue, serverID), nil
}
func (m *memStore) InsertQueueSession(_ context.Context, s presence.Session) (int64, error) {
	m.nextID++
	s.ID = m.nextID
	m.queue = append(m.queue, s)
	return s.ID, nil
}
func (m *memStore) UpdateQueueSession(_ context.Context, s presence.Session) error {
	for i := range m.queue {
		if m.queue[i].ID == s.ID {
			m.queue[i] = s
		}
	}
	return nil
}
func (m *memStore) AppendEvent(_ context.Context, e presence.Event) error {
	m.events = append(m.events, e)
	return nil
}

func (m *memStore) eventsOf(t presence.EventType) []presence.Event {
	var out []presence.Event
	for _, e := range m.events {
		if e.Type == t {
			out = append(out, e)
		}
	}
	return out
}

func pl(id, name string) bohemia.Player {
	return bohemia.Player{UserID: id, Username: name, GameClientType: "PLATFORM_PC", PlatformUserID: "7656" + id}
}

var t0 = time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)

// poll — успешное наблюдение сервера 1 в момент t0+minute минут; после него фиксируем «последний успешный poll».
func poll(t *testing.T, svc *presence.Service, m *memStore, minute int, connected, queue []bohemia.Player) presence.Result {
	t.Helper()
	at := t0.Add(time.Duration(minute) * time.Minute)
	res, err := svc.Apply(context.Background(), presence.Observation{ServerID: 1, ObservedAt: at, Connected: connected, Queue: queue})
	if err != nil {
		t.Fatalf("poll at +%dm: %v", minute, err)
	}
	m.lastPoll[1] = at
	return res
}

func TestApply_joinThenConfirmedLeave(t *testing.T) {
	m := newMem()
	svc := presence.NewService(m, presence.Config{AbsentConfirmations: 2, MaxGap: 15 * time.Minute})

	res := poll(t, svc, m, 0, []bohemia.Player{pl("a", "Alice"), pl("b", "Bob")}, nil)
	if res.Joined != 2 || res.NewPlayers != 2 || res.Players != 2 {
		t.Fatalf("first poll: %+v", res)
	}

	// +1: Bob исчез один раз → SUSPECTED_GONE, событий нет.
	res = poll(t, svc, m, 1, []bohemia.Player{pl("a", "Alice")}, nil)
	if res.SuspectedGone != 1 || res.Left != 0 {
		t.Fatalf("second poll: %+v", res)
	}
	if n := len(m.eventsOf(presence.EventPlayerLeftServer)); n != 0 {
		t.Fatalf("no LEFT yet, got %d", n)
	}

	// +2: Bob отсутствует второй раз → CLOSED_LEFT, ended_at = first_known_absent_at = +1.
	res = poll(t, svc, m, 2, []bohemia.Player{pl("a", "Alice")}, nil)
	if res.Left != 1 {
		t.Fatalf("third poll: %+v", res)
	}
	bob := m.presence[1]
	if bob.Status != presence.StatusClosedLeft || bob.EndedAt == nil || !bob.EndedAt.Equal(t0.Add(time.Minute)) || !bob.LastSeenAt.Equal(t0) {
		t.Errorf("bob session: %+v", bob)
	}
	left := m.eventsOf(presence.EventPlayerLeftServer)
	if len(left) != 1 || !left[0].OccurredAt.Equal(t0.Add(time.Minute)) {
		t.Errorf("LEFT event: %+v", left)
	}
}

func TestApply_reappearDuringSuspected(t *testing.T) {
	m := newMem()
	svc := presence.NewService(m, presence.Config{AbsentConfirmations: 2})

	poll(t, svc, m, 0, []bohemia.Player{pl("a", "Alice")}, nil)
	poll(t, svc, m, 1, nil, nil)                                       // пропала один раз
	res := poll(t, svc, m, 2, []bohemia.Player{pl("a", "Alice")}, nil) // вернулась

	if res.Joined != 0 || len(m.presence) != 1 {
		t.Fatalf("must reuse the same session: %+v, sessions=%d", res, len(m.presence))
	}
	s := m.presence[0]
	if s.Status != presence.StatusOnline || s.AbsentPolls != 0 || s.FirstKnownAbsentAt != nil {
		t.Errorf("session after return: %+v", s)
	}
}

func TestApply_dataGapClosesWithoutLeaveEvent(t *testing.T) {
	m := newMem()
	svc := presence.NewService(m, presence.Config{AbsentConfirmations: 2, MaxGap: 15 * time.Minute})

	poll(t, svc, m, 0, []bohemia.Player{pl("a", "Alice")}, nil)
	// 30 минут без успешных poll, потом Alice снова в списке.
	res := poll(t, svc, m, 30, []bohemia.Player{pl("a", "Alice")}, nil)

	if res.DataGapClosed != 1 || res.Joined != 1 || !res.AfterDataGap {
		t.Fatalf("after gap: %+v", res)
	}
	old := m.presence[0]
	if old.Status != presence.StatusClosedDataGap || !old.EndedAt.Equal(t0) {
		t.Errorf("old session: %+v", old)
	}
	if n := len(m.eventsOf(presence.EventPlayerLeftServer)); n != 0 {
		t.Errorf("data gap must not emit LEFT, got %d", n)
	}
	joins := m.eventsOf(presence.EventPlayerJoinedServer)
	if len(joins) != 2 || !joins[1].AfterDataGap {
		t.Errorf("second JOIN must be flagged after_data_gap: %+v", joins)
	}
}

func TestApply_startupReplayFlag(t *testing.T) {
	m := newMem()
	svc := presence.NewService(m, presence.Config{})
	_, err := svc.Apply(context.Background(), presence.Observation{ServerID: 1, ObservedAt: t0, Connected: []bohemia.Player{pl("a", "Alice")}, StartupReplay: true})
	if err != nil {
		t.Fatal(err)
	}
	if !m.presence[0].StartupReplay || !m.events[0].StartupReplay {
		t.Errorf("startup replay flag lost: %+v %+v", m.presence[0], m.events[0])
	}
}

func TestApply_nicknameChange(t *testing.T) {
	m := newMem()
	svc := presence.NewService(m, presence.Config{})
	poll(t, svc, m, 0, []bohemia.Player{pl("a", "Alice")}, nil)
	res := poll(t, svc, m, 1, []bohemia.Player{pl("a", "Alicia")}, nil)

	if res.NicknameChanges != 1 {
		t.Fatalf("result: %+v", res)
	}
	ev := m.eventsOf(presence.EventPlayerNicknameChanged)
	if len(ev) != 1 || ev[0].Payload["old"] != "Alice" || ev[0].Payload["new"] != "Alicia" {
		t.Errorf("nickname event: %+v", ev)
	}
	if m.presence[0].Nickname != "Alicia" {
		t.Errorf("session nickname must follow the player: %+v", m.presence[0])
	}
}

func TestApply_queueThenJoin(t *testing.T) {
	m := newMem()
	svc := presence.NewService(m, presence.Config{AbsentConfirmations: 2})

	res := poll(t, svc, m, 0, nil, []bohemia.Player{pl("q", "Quinn")})
	if res.QueueEntered != 1 || res.Queued != 1 {
		t.Fatalf("enter queue: %+v", res)
	}
	poll(t, svc, m, 1, nil, []bohemia.Player{pl("q", "Quinn")})
	// +2: Quinn пропал из очереди и появился в игре.
	res = poll(t, svc, m, 2, []bohemia.Player{pl("q", "Quinn")}, nil)
	if res.Joined != 1 || res.QueueClosed != 0 {
		t.Fatalf("+2: %+v", res) // очередь ещё SUSPECTED_GONE (одно отсутствие)
	}
	res = poll(t, svc, m, 3, []bohemia.Player{pl("q", "Quinn")}, nil)
	if res.QueueClosed != 1 {
		t.Fatalf("+3: %+v", res)
	}
	q := m.queue[0]
	if q.QueueResult != presence.QueueJoinedServer || q.Status != presence.StatusClosedLeft {
		t.Errorf("queue session: %+v", q)
	}
	ev := m.eventsOf(presence.EventPlayerLeftQueue)
	if len(ev) != 1 || ev[0].Payload["result"] != "JOINED_SERVER" || ev[0].Payload["waited_seconds"] != 60 {
		t.Errorf("queue event: %+v", ev)
	}
}

func TestApply_duplicatePlayerInList(t *testing.T) {
	m := newMem()
	svc := presence.NewService(m, presence.Config{})
	res := poll(t, svc, m, 0, []bohemia.Player{pl("a", "Alice"), pl("a", "Alice")}, nil)
	if res.Players != 1 || res.Joined != 1 || len(m.presence) != 1 {
		t.Errorf("duplicates must collapse: %+v", res)
	}
}
