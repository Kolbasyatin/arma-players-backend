package steam_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"armaplayers/internal/steam"
)

// Фикстуры — НАСТОЯЩИЕ ответы Valve, снятые 22.09.2026 по двум живым аккаунтам.
// Придуманные тут не годятся: половина тонкостей (PascalCase у банов, пустой объект
// вместо ошибки у скрытых игр) в документации не описана и обнаружилась только на данных.
func load(t *testing.T, name string) steam.Snapshot {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var snapshot steam.Snapshot
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestParseProfile_gamesHidden(t *testing.T) {
	profile := steam.ParseProfile(load(t, "snapshot_games_hidden.json"))

	if profile.PersonaName != "Шустрый" {
		t.Errorf("persona: got %q", profile.PersonaName)
	}
	if profile.RealName != "Патриот(" {
		t.Errorf("real name: got %q", profile.RealName)
	}
	if profile.Visibility == nil || *profile.Visibility != 3 {
		t.Errorf("visibility: got %v, профиль открыт", profile.Visibility)
	}
	if profile.AccountCreatedAt == nil {
		t.Fatal("дата создания аккаунта не разобрана")
	}
	if got := profile.AccountCreatedAt.UTC().Format(time.RFC3339); got != "2026-05-26T05:27:40Z" {
		t.Errorf("создан: got %s", got)
	}

	// Главное в этом наборе: профиль открыт, а игры скрыты. Valve отвечает HTTP 200
	// и {"response":{}}, поэтому по коду ответа этого не понять — только по отсутствию game_count.
	if profile.GamesVisible {
		t.Error("games_visible: у этого игрока игры скрыты")
	}
	if profile.ReforgerMinutes != nil {
		t.Errorf("наигранное должно быть неизвестно, got %v", *profile.ReforgerMinutes)
	}

	if !profile.FriendsVisible {
		t.Error("friends_visible: список друзей у него открыт")
	}
	if profile.VACBanned == nil || *profile.VACBanned {
		t.Errorf("vac: got %v, ожидали явное «нет»", profile.VACBanned)
	}
	if profile.EconomyBan != "none" {
		t.Errorf("economy ban: got %q", profile.EconomyBan)
	}
}

func TestParseProfile_gamesVisible(t *testing.T) {
	profile := steam.ParseProfile(load(t, "snapshot_games_visible.json"))

	if !profile.GamesVisible {
		t.Fatal("games_visible: игры у этого игрока открыты")
	}
	if profile.ReforgerMinutes == nil || *profile.ReforgerMinutes != 51648 {
		t.Errorf("наиграно всего: got %v, want 51648", profile.ReforgerMinutes)
	}
	if profile.ReforgerMinutes2W == nil || *profile.ReforgerMinutes2W != 1230 {
		t.Errorf("наиграно за 2 недели: got %v, want 1230", profile.ReforgerMinutes2W)
	}

	// Неудавшийся источник не должен ронять остальные: summary здесь отдал HTTP 500,
	// а баны не пришли вовсе — но игры и друзья разобраны.
	if profile.PersonaName != "" {
		t.Errorf("summary не приходил, ник должен быть пуст, got %q", profile.PersonaName)
	}
	if profile.VACBanned != nil {
		t.Error("баны не приходили — значение должно остаться неизвестным, а не false")
	}
}

func TestParseFriends(t *testing.T) {
	friends, visible := steam.ParseFriends(load(t, "snapshot_games_hidden.json"))

	if !visible {
		t.Fatal("список друзей открыт")
	}
	if len(friends) != 3 {
		t.Fatalf("друзей: want 3, got %d", len(friends))
	}
	if friends[0].SteamID != "76561198691588658" {
		t.Errorf("первый друг: got %q", friends[0].SteamID)
	}
	if friends[0].Since == nil {
		t.Fatal("friend_since не разобран")
	}
	if got := friends[0].Since.UTC().Format("2006-01-02"); got != "2026-09-18" {
		t.Errorf("дружат с: got %s", got)
	}
}

func TestParseFriends_hidden(t *testing.T) {
	snapshot := steam.Snapshot{
		SteamID: "76561198000000000",
		Sources: []steam.Source{{Name: steam.SourceFriends, Error: "HTTP 401"}},
	}

	friends, visible := steam.ParseFriends(snapshot)

	// Скрытый список и пустой список — разные вещи: во втором случае затирать
	// уже известные связи нельзя, иначе одна закрытая настройка сотрёт весь граф.
	if visible {
		t.Error("закрытый список не должен считаться видимым")
	}
	if friends != nil {
		t.Errorf("друзей быть не должно, got %v", friends)
	}
}
