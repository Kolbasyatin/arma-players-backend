package steam

import (
	"context"
	"errors"

	"log/slog"
	"time"
)

// Store — то, что нужно сервису от хранилища. Интерфейс объявлен здесь, у потребителя.
type Store interface {
	SaveSnapshot(ctx context.Context, steamID, source string, fetchedAt time.Time, body []byte) (bool, error)
	SaveProfile(ctx context.Context, profile Profile) error
	SaveFriends(ctx context.Context, steamID string, friends []Friend, seenAt time.Time) error
	Watchlist(ctx context.Context, policy WatchlistPolicy, now time.Time, limit int) ([]string, error)
	MarkTry(ctx context.Context, steamID string, at time.Time, errText string) error
}

// DossierStore — то, что нужно для чтения досье. Отдельно от Store: фоновый цикл этих
// методов не касается, а собирать всё в один интерфейс значит заставлять реализацию
// поддерживать то, что ей не нужно.
type DossierStore interface {
	Store
	SteamIDsOfPlayer(ctx context.Context, playerID int64) ([]string, error)
	Profile(ctx context.Context, steamID string) (StoredProfile, bool, error)
	Friends(ctx context.Context, steamID string) ([]KnownFriend, error)
	AddToWatchlist(ctx context.Context, steamID, addedBy, note string) error
}

// Fetcher — шлюз. Отдельным интерфейсом, чтобы сервис тестировался без сети.
type Fetcher interface {
	Fetch(ctx context.Context, steamID string) (Snapshot, error)
}

type Service struct {
	fetcher Fetcher
	store   Store
	log     *slog.Logger
	now     func() time.Time
}

func NewService(fetcher Fetcher, store Store, log *slog.Logger) *Service {
	return &Service{fetcher: fetcher, store: store, log: log, now: time.Now}
}

// Enrich собирает данные по одному игроку и раскладывает их.
//
// Порядок важен: сначала сырьё, потом производные. Если разбор сломается на изменившейся
// форме, снимок уже в базе — починим извлечение и пересчитаем, не дожидаясь Valve.
func (s *Service) Enrich(ctx context.Context, steamID string) error {
	at := s.now().UTC()

	snapshot, err := s.fetcher.Fetch(ctx, steamID)
	if err != nil {
		_ = s.store.MarkTry(ctx, steamID, at, err.Error())
		return err
	}

	stored := 0
	for _, source := range snapshot.Sources {
		if len(source.Body) == 0 {
			continue
		}
		changed, err := s.store.SaveSnapshot(ctx, steamID, source.Name, snapshot.FetchedAt, source.Body)
		if err != nil {
			_ = s.store.MarkTry(ctx, steamID, at, err.Error())
			return err
		}
		if changed {
			stored++
		}
	}

	if err := s.store.SaveProfile(ctx, ParseProfile(snapshot)); err != nil {
		_ = s.store.MarkTry(ctx, steamID, at, err.Error())
		return err
	}

	// Граф трогаем ТОЛЬКО при видимом списке: у закрытого профиля пустой список
	// пометил бы все известные связи потерянными.
	if friends, visible := ParseFriends(snapshot); visible {
		if err := s.store.SaveFriends(ctx, steamID, friends, snapshot.FetchedAt); err != nil {
			_ = s.store.MarkTry(ctx, steamID, at, err.Error())
			return err
		}
	}

	s.log.Info("steam: enriched", "steam_id", steamID, "snapshots_written", stored)

	return s.store.MarkTry(ctx, steamID, at, "")
}

// WatchlistPolicy — как часто переопрашивать игрока. Частота привязана к тому, играет ли он:
// дата создания аккаунта неизменна, баны меняются раз в жизни, а playtime_2weeks — каждый день,
// но только у активного. Опрашивать всех одинаково значит тратить квоту на неменяющееся.
type WatchlistPolicy struct {
	// ActiveWindow — за какой срок игрок должен был появиться на наших серверах, чтобы
	// считаться активным.
	ActiveWindow time.Duration
	// ActiveStaleness / IdleStaleness — через сколько данные считаются устаревшими
	// для активного и для неактивного соответственно.
	ActiveStaleness time.Duration
	IdleStaleness   time.Duration
}

// LoopConfig — параметры фонового обхода.
type LoopConfig struct {
	Interval time.Duration
	Batch    int
	Policy   WatchlistPolicy
}

// RunLoop раз в Interval обходит устаревших из watchlist.
//
// Пачка РАЗМАЗЫВАЕТСЯ по интервалу, а не выполняется залпом: пятьдесят игроков подряд —
// это двести запросов к Valve за несколько секунд и час тишины после. Ровный поток чужой
// сервис переносит спокойно, всплеск — повод получить 429. Тот же приём, что в tracking,
// где опросы серверов распределены по минуте.
//
// Ошибка одного игрока не останавливает остальных: закрытый профиль или сбой Valve —
// обычное дело, и из-за него нельзя терять весь проход.
func (s *Service) RunLoop(ctx context.Context, cfg LoopConfig) error {
	batch := cfg.Batch
	if batch < 1 {
		batch = 1
	}
	// Шаг подбирается так, чтобы полная пачка заняла ровно интервал.
	pace := cfg.Interval / time.Duration(batch)

	for {
		startedAt := s.now()

		ids, err := s.store.Watchlist(ctx, cfg.Policy, startedAt.UTC(), batch)
		if err != nil {
			s.log.Error("steam: watchlist", "err", err)
		}

		for i, id := range ids {
			if i > 0 {
				if err := sleep(ctx, pace); err != nil {
					return err
				}
			}
			if err := s.Enrich(ctx, id); err != nil {
				s.log.Warn("steam: enrich failed", "steam_id", id, "err", err)
			}
		}

		// Досыпаем остаток интервала: если игроков было меньше пачки, проход закончился раньше.
		if err := sleep(ctx, time.Until(startedAt.Add(cfg.Interval))); err != nil {
			return err
		}
	}
}

func sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// ErrNoSteamAccount — у игрока не наблюдалось Steam-аккаунта. Не ошибка сервиса:
// в Reforger играют и с консолей, а платформенный id приходит от Bohemia не всегда.
var ErrNoSteamAccount = errors.New("steam: player has no known steam account")

// DossierMaxAge — насколько старым данным доверяем при показе досье. Свежее — отдаём как есть,
// старше — собираем прямо сейчас, не заставляя человека ждать следующего фонового прохода.
const DossierMaxAge = 6 * time.Hour

// DossierCollectTimeout — предел синхронного сбора. Нужен потому, что цепочка длинная:
// мы → шлюз → четыре метода Valve. Без предела запрос висел бы, пока не сдастся клиент,
// и человек получил бы таймаут вместо ответа. С пределом он получает то, что успели собрать,
// а остальное доберёт фоновый обход.
const DossierCollectTimeout = 20 * time.Second

// Dossier отдаёт всё, что мы знаем об игроке со стороны Steam, и ПОПУТНО ставит его
// в watchlist. Смысл в том, что интерес человека — лучший признак «этот игрок нам важен»:
// заводить отдельную команду «начни собирать досье» незачем.
//
// При первом обращении данных ещё нет, поэтому сбор делается синхронно: иначе команда
// в боте вернула бы пустоту и предложение зайти через час.
func (s *Service) Dossier(ctx context.Context, store DossierStore, playerID int64) (Dossier, error) {
	ids, err := store.SteamIDsOfPlayer(ctx, playerID)
	if err != nil {
		return Dossier{}, err
	}
	if len(ids) == 0 {
		return Dossier{}, ErrNoSteamAccount
	}
	steamID := ids[0]

	if err := store.AddToWatchlist(ctx, steamID, "dossier", ""); err != nil {
		return Dossier{}, err
	}

	profile, found, err := store.Profile(ctx, steamID)
	if err != nil {
		return Dossier{}, err
	}

	dossier := Dossier{PlayerID: playerID, SteamID: steamID}

	if !found || s.now().UTC().Sub(profile.UpdatedAt) > DossierMaxAge {
		collectCtx, cancel := context.WithTimeout(ctx, DossierCollectTimeout)
		err := s.Enrich(collectCtx, steamID)
		cancel()

		if err != nil {
			// Сбор не удался — отдаём то, что есть. Устаревшие данные полезнее ошибки,
			// а их возраст виден в UpdatedAt. Причину передаём наверх: без неё пустое
			// досье выглядит как «у игрока всё скрыто», хотя на деле мы просто не дошли до Valve.
			s.log.Warn("steam: dossier enrich failed", "steam_id", steamID, "err", err)
			dossier.LastError = err.Error()
		} else {
			dossier.Collected = true
			if profile, found, err = store.Profile(ctx, steamID); err != nil {
				return Dossier{}, err
			}
		}
	}

	// Профиля нет — значит собрать ещё ни разу не удалось. Отдаём пустое досье с причиной,
	// а не структуру с нулевыми полями: та неотличима от «всё скрыто».
	if !found {
		return dossier, nil
	}

	dossier.Profile = &profile

	friends, err := store.Friends(ctx, steamID)
	if err != nil {
		return Dossier{}, err
	}
	dossier.Friends = friends
	for _, friend := range friends {
		if friend.PlayerID != nil {
			dossier.FriendsKnown++
		}
	}

	return dossier, nil
}
