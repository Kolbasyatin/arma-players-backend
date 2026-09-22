package steam_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"armaplayers/internal/steam"
)

type fakeStore struct {
	steamIDs  map[int64][]string
	profile   map[string]steam.StoredProfile
	watchlist []string
	snapshots int
	friends   []steam.KnownFriend
	saved     int
}

func (f *fakeStore) SaveSnapshot(_ context.Context, _, _ string, _ time.Time, _ []byte) (bool, error) {
	f.snapshots++
	return true, nil
}

func (f *fakeStore) SaveProfile(_ context.Context, p steam.Profile) error {
	f.saved++
	// Имитируем базу: после сбора профиль появляется и он свежий.
	f.profile[p.SteamID] = steam.StoredProfile{
		SteamID: p.SteamID, PersonaName: p.PersonaName, UpdatedAt: time.Now().UTC(),
	}
	return nil
}

func (f *fakeStore) SaveFriends(context.Context, string, []steam.Friend, time.Time) error { return nil }

func (f *fakeStore) Watchlist(context.Context, steam.WatchlistPolicy, time.Time, int) ([]string, error) {
	return nil, nil
}

func (f *fakeStore) MarkTry(context.Context, string, time.Time, string) error { return nil }

func (f *fakeStore) SteamIDsOfPlayer(_ context.Context, playerID int64) ([]string, error) {
	return f.steamIDs[playerID], nil
}

func (f *fakeStore) Profile(_ context.Context, steamID string) (steam.StoredProfile, bool, error) {
	p, ok := f.profile[steamID]
	return p, ok, nil
}

func (f *fakeStore) Friends(context.Context, string) ([]steam.KnownFriend, error) {
	return f.friends, nil
}

func (f *fakeStore) AddToWatchlist(_ context.Context, steamID, _, _ string) error {
	f.watchlist = append(f.watchlist, steamID)
	return nil
}

type fakeFetcher struct {
	calls int
	err   error
}

func (f *fakeFetcher) Fetch(_ context.Context, steamID string) (steam.Snapshot, error) {
	f.calls++
	if f.err != nil {
		return steam.Snapshot{}, f.err
	}
	return steam.Snapshot{
		SteamID:   steamID,
		FetchedAt: time.Now().UTC(),
		Sources: []steam.Source{
			{Name: steam.SourceSummary, Body: []byte(`{"response":{"players":[{"personaname":"Шустрый"}]}}`)},
		},
	}, nil
}

func newService(store *fakeStore, fetcher *fakeFetcher) *steam.Service {
	return steam.NewService(fetcher, store, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestDossier_firstCallCollectsAndRegisters(t *testing.T) {
	store := &fakeStore{
		steamIDs: map[int64][]string{7: {"76561198884181842"}},
		profile:  map[string]steam.StoredProfile{},
	}
	fetcher := &fakeFetcher{}

	dossier, err := newService(store, fetcher).Dossier(context.Background(), store, 7)
	if err != nil {
		t.Fatal(err)
	}

	// Данных ещё не было — значит собираем прямо сейчас, а не отправляем человека ждать час.
	if !dossier.Collected {
		t.Error("при первом обращении данные должны собираться синхронно")
	}
	if fetcher.calls != 1 {
		t.Errorf("походов к шлюзу: want 1, got %d", fetcher.calls)
	}
	if dossier.Profile.PersonaName != "Шустрый" {
		t.Errorf("профиль не заполнен: %+v", dossier.Profile)
	}

	// И игрок сам собой попал в список на регулярное обновление.
	if len(store.watchlist) != 1 || store.watchlist[0] != "76561198884181842" {
		t.Errorf("игрок не добавлен в watchlist: %v", store.watchlist)
	}
}

func TestDossier_freshDataIsNotRefetched(t *testing.T) {
	store := &fakeStore{
		steamIDs: map[int64][]string{7: {"111"}},
		profile:  map[string]steam.StoredProfile{"111": {SteamID: "111", PersonaName: "Свежий", UpdatedAt: time.Now().UTC()}},
	}
	fetcher := &fakeFetcher{}

	dossier, err := newService(store, fetcher).Dossier(context.Background(), store, 7)
	if err != nil {
		t.Fatal(err)
	}

	if fetcher.calls != 0 {
		t.Errorf("свежие данные не должны вызывать поход наружу, походов: %d", fetcher.calls)
	}
	if dossier.Collected {
		t.Error("collected должен быть false: ничего не собирали")
	}
	if dossier.Profile.PersonaName != "Свежий" {
		t.Errorf("отдан не сохранённый профиль: %+v", dossier.Profile)
	}
}

func TestDossier_staleDataReturnedWhenGatewayFails(t *testing.T) {
	old := time.Now().UTC().Add(-48 * time.Hour)
	store := &fakeStore{
		steamIDs: map[int64][]string{7: {"111"}},
		profile:  map[string]steam.StoredProfile{"111": {SteamID: "111", PersonaName: "Старый", UpdatedAt: old}},
	}
	fetcher := &fakeFetcher{err: errors.New("gateway down")}

	dossier, err := newService(store, fetcher).Dossier(context.Background(), store, 7)

	// Шлюз лёг — это не повод ответить ошибкой: устаревшие данные полезнее пустоты,
	// а их возраст виден в updated_at.
	if err != nil {
		t.Fatalf("отказ шлюза не должен ронять досье: %v", err)
	}
	if dossier.Profile.PersonaName != "Старый" {
		t.Errorf("не отдан сохранённый профиль: %+v", dossier.Profile)
	}
	if dossier.Collected {
		t.Error("collected должен быть false: сбор не удался")
	}
}

func TestDossier_playerWithoutSteamAccount(t *testing.T) {
	store := &fakeStore{steamIDs: map[int64][]string{}, profile: map[string]steam.StoredProfile{}}

	_, err := newService(store, &fakeFetcher{}).Dossier(context.Background(), store, 7)

	// Консольные игроки — обычное дело, это ответ, а не сбой.
	if !errors.Is(err, steam.ErrNoSteamAccount) {
		t.Errorf("want ErrNoSteamAccount, got %v", err)
	}
	if len(store.watchlist) != 0 {
		t.Error("без Steam-аккаунта в watchlist добавлять нечего")
	}
}

func TestDossier_countsFriendsKnownToUs(t *testing.T) {
	playerID := int64(42)
	store := &fakeStore{
		steamIDs: map[int64][]string{7: {"111"}},
		profile:  map[string]steam.StoredProfile{"111": {SteamID: "111", UpdatedAt: time.Now().UTC()}},
		friends: []steam.KnownFriend{
			{SteamID: "222", PlayerID: &playerID, Nickname: "Сосед"},
			{SteamID: "333"},
			{SteamID: "444"},
		},
	}

	dossier, err := newService(store, &fakeFetcher{}).Dossier(context.Background(), store, 7)
	if err != nil {
		t.Fatal(err)
	}

	if len(dossier.Friends) != 3 {
		t.Errorf("друзей: want 3, got %d", len(dossier.Friends))
	}
	// Смысл графа именно в этом числе: сколько его друзей мы сами видели на серверах.
	if dossier.FriendsKnown != 1 {
		t.Errorf("знакомых друзей: want 1, got %d", dossier.FriendsKnown)
	}
}
