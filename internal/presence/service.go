package presence

import (
	"context"
	"fmt"
	"time"

	"armaplayers/internal/bohemia"
)

// Service применяет наблюдения к состоянию.
type Service struct {
	store Store
	cfg   Config
}

func NewService(store Store, cfg Config) *Service {
	return &Service{store: store, cfg: cfg.withDefaults()}
}

// Apply — одна транзакция на одно наблюдение: игроки, сессии присутствия, сессии очереди, события.
func (s *Service) Apply(ctx context.Context, obs Observation) (Result, error) {
	var res Result
	err := s.store.WithTx(ctx, func(tx Tx) error {
		var err error
		res, err = s.apply(ctx, tx, obs)
		return err
	})
	return res, err
}

func (s *Service) apply(ctx context.Context, tx Tx, obs Observation) (Result, error) {
	var res Result
	now := obs.ObservedAt

	// Разрыв данных: с последнего успешного poll этого сервера прошло больше MaxGap.
	lastOK, ok, err := tx.LastSuccessfulListPlayersAt(ctx, obs.ServerID)
	if err != nil {
		return res, fmt.Errorf("presence: last poll: %w", err)
	}
	res.AfterDataGap = ok && now.Sub(lastOK) > s.cfg.MaxGap

	// Идентичности: один upsert на игрока, независимо от того, в игре он или в очереди.
	refs := map[string]PlayerRef{} // bohemia userId → ref
	upsert := func(players []bohemia.Player) error {
		for _, p := range players {
			if p.UserID == "" {
				continue
			}
			if _, done := refs[p.UserID]; done {
				continue
			}
			ref, err := tx.UpsertPlayer(ctx, p, now)
			if err != nil {
				return fmt.Errorf("presence: upsert player %s: %w", p.UserID, err)
			}
			refs[p.UserID] = ref
			if ref.IsNew {
				res.NewPlayers++
			}
			if !ref.IsNew && ref.PreviousNickname != "" && ref.PreviousNickname != p.Username {
				res.NicknameChanges++
				if err := tx.AppendEvent(ctx, Event{
					Type: EventPlayerNicknameChanged, OccurredAt: now, PlayerID: ref.PlayerID, ServerID: obs.ServerID,
					Payload:       map[string]any{"old": ref.PreviousNickname, "new": p.Username},
					StartupReplay: obs.StartupReplay, AfterDataGap: res.AfterDataGap,
				}); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := upsert(obs.Connected); err != nil {
		return res, err
	}
	if err := upsert(obs.Queue); err != nil {
		return res, err
	}

	connectedIDs := playerIDs(obs.Connected, refs)
	queueIDs := playerIDs(obs.Queue, refs)
	nicknames := nicknamesByPlayer(obs, refs)
	res.Players, res.Queued = len(connectedIDs), len(queueIDs)

	// Присутствие.
	open, err := tx.OpenPresenceSessions(ctx, obs.ServerID)
	if err != nil {
		return res, fmt.Errorf("presence: open sessions: %w", err)
	}
	stats, err := s.reconcile(ctx, tx, obs, res.AfterDataGap, open, connectedIDs, nicknames, presenceOps{})
	if err != nil {
		return res, err
	}
	res.Joined, res.Left, res.SuspectedGone, res.DataGapClosed = stats.opened, stats.closedLeft, stats.suspected, stats.closedGap

	// Очередь: та же машина состояний, другие таблица/события и итог при закрытии.
	openQ, err := tx.OpenQueueSessions(ctx, obs.ServerID)
	if err != nil {
		return res, fmt.Errorf("presence: open queue sessions: %w", err)
	}
	qstats, err := s.reconcile(ctx, tx, obs, res.AfterDataGap, openQ, queueIDs, nicknames, queueOps{connected: connectedIDs})
	if err != nil {
		return res, err
	}
	res.QueueEntered, res.QueueClosed = qstats.opened, qstats.closedLeft+qstats.closedGap
	res.DataGapClosed += qstats.closedGap

	return res, nil
}

// playerIDs — множество внутренних id игроков из списка API (дубли схлопываются).
func playerIDs(players []bohemia.Player, refs map[string]PlayerRef) map[int64]struct{} {
	ids := make(map[int64]struct{}, len(players))
	for _, p := range players {
		if ref, ok := refs[p.UserID]; ok {
			ids[ref.PlayerID] = struct{}{}
		}
	}
	return ids
}

// nicknamesByPlayer — текущий ник каждого игрока из наблюдения по внутреннему id.
func nicknamesByPlayer(obs Observation, refs map[string]PlayerRef) map[int64]string {
	out := make(map[int64]string, len(refs))
	for _, list := range [][]bohemia.Player{obs.Connected, obs.Queue} {
		for _, p := range list {
			if ref, ok := refs[p.UserID]; ok {
				out[ref.PlayerID] = p.Username
			}
		}
	}
	return out
}

type reconcileStats struct{ opened, suspected, closedLeft, closedGap int }

// sessionOps — чем присутствие отличается от очереди: куда писать и какие события порождать.
type sessionOps interface {
	insert(ctx context.Context, tx Tx, s Session) (int64, error)
	update(ctx context.Context, tx Tx, s Session) error
	openedEvent() EventType
	closedEvent() EventType
	// onClose дополняет сессию при закрытии (итог очереди) и возвращает payload события.
	onClose(s *Session, gap bool) map[string]any
}

// reconcile — машина состояний ADR 0005 для одного вида сессий.
func (s *Service) reconcile(ctx context.Context, tx Tx, obs Observation, afterGap bool, open []Session, present map[int64]struct{}, nicknames map[int64]string, ops sessionOps) (reconcileStats, error) {
	var st reconcileStats
	now := obs.ObservedAt
	seen := make(map[int64]bool, len(open))

	for i := range open {
		sess := open[i]
		seen[sess.PlayerID] = true

		// Сессия пережила разрыв данных: закрываем по last_seen_at без события выхода.
		if now.Sub(sess.LastSeenAt) > s.cfg.MaxGap {
			sess.Status = StatusClosedDataGap
			ended := sess.LastSeenAt
			sess.EndedAt = &ended
			ops.onClose(&sess, true)
			if err := ops.update(ctx, tx, sess); err != nil {
				return st, err
			}
			st.closedGap++
			delete(seen, sess.PlayerID) // игрок, если он сейчас в списке, получит новую сессию
			continue
		}

		if _, here := present[sess.PlayerID]; here {
			sess.Status, sess.LastSeenAt, sess.AbsentPolls, sess.FirstKnownAbsentAt = StatusOnline, now, 0, nil
			sess.Nickname = nicknames[sess.PlayerID]
			if err := ops.update(ctx, tx, sess); err != nil {
				return st, err
			}
			continue
		}

		// Отсутствует в успешном наблюдении.
		sess.AbsentPolls++
		if sess.FirstKnownAbsentAt == nil {
			t := now
			sess.FirstKnownAbsentAt = &t
		}
		if sess.AbsentPolls >= s.cfg.AbsentConfirmations {
			sess.Status = StatusClosedLeft
			sess.EndedAt = sess.FirstKnownAbsentAt
			payload := ops.onClose(&sess, false)
			if err := ops.update(ctx, tx, sess); err != nil {
				return st, err
			}
			st.closedLeft++
			id := sess.ID
			if err := tx.AppendEvent(ctx, Event{
				Type: ops.closedEvent(), OccurredAt: *sess.FirstKnownAbsentAt, PlayerID: sess.PlayerID, ServerID: obs.ServerID,
				SessionID: &id, Payload: payload, AfterDataGap: false,
			}); err != nil {
				return st, err
			}
			continue
		}
		sess.Status = StatusSuspectedGone
		if err := ops.update(ctx, tx, sess); err != nil {
			return st, err
		}
		st.suspected++
	}

	// Новые: присутствуют, но открытой сессии нет.
	for playerID := range present {
		if seen[playerID] {
			continue
		}
		sess := Session{
			PlayerID: playerID, ServerID: obs.ServerID, FirstSeenAt: now, LastSeenAt: now,
			Status: StatusOnline, StartupReplay: obs.StartupReplay, Nickname: nicknames[playerID],
		}
		id, err := ops.insert(ctx, tx, sess)
		if err != nil {
			return st, err
		}
		st.opened++
		if err := tx.AppendEvent(ctx, Event{
			Type: ops.openedEvent(), OccurredAt: now, PlayerID: playerID, ServerID: obs.ServerID, SessionID: &id,
			StartupReplay: obs.StartupReplay, AfterDataGap: afterGap,
		}); err != nil {
			return st, err
		}
	}
	return st, nil
}

type presenceOps struct{}

func (presenceOps) insert(ctx context.Context, tx Tx, s Session) (int64, error) {
	return tx.InsertPresenceSession(ctx, s)
}
func (presenceOps) update(ctx context.Context, tx Tx, s Session) error {
	return tx.UpdatePresenceSession(ctx, s)
}
func (presenceOps) openedEvent() EventType                { return EventPlayerJoinedServer }
func (presenceOps) closedEvent() EventType                { return EventPlayerLeftServer }
func (presenceOps) onClose(*Session, bool) map[string]any { return nil }

type queueOps struct{ connected map[int64]struct{} }

func (queueOps) insert(ctx context.Context, tx Tx, s Session) (int64, error) {
	return tx.InsertQueueSession(ctx, s)
}
func (queueOps) update(ctx context.Context, tx Tx, s Session) error {
	return tx.UpdateQueueSession(ctx, s)
}
func (queueOps) openedEvent() EventType { return EventPlayerEnteredQueue }
func (queueOps) closedEvent() EventType { return EventPlayerLeftQueue }

// onClose: очередь закрылась — игрок либо вошёл на сервер (сейчас в connected), либо ушёл.
func (q queueOps) onClose(s *Session, gap bool) map[string]any {
	switch {
	case gap:
		s.QueueResult = QueueUnknown
	default:
		if _, joined := q.connected[s.PlayerID]; joined {
			s.QueueResult = QueueJoinedServer
		} else {
			s.QueueResult = QueueLeft
		}
	}
	return map[string]any{"result": string(s.QueueResult), "waited_seconds": int(s.LastSeenAt.Sub(s.FirstSeenAt) / time.Second)}
}
