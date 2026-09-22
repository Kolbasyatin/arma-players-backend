package steam_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"armaplayers/internal/steam"
)

// Форма ответа взята с C#-записей arma-reforger-hz:
//
//	SteamProfileSnapshot(string SteamId, DateTimeOffset FetchedAt, IReadOnlyList<SteamSource> Sources)
//	SteamSource(string Source, JsonElement? Body, string? Error)
//
// ASP.NET сериализует свойства в camelCase по умолчанию, политика имён в Program.cs не задана.
// Ровно так же устроен ответ /token, и Go-клиент токена давно читает `accessToken`.
const gatewayResponse = `{
  "steamId": "76561198884181842",
  "fetchedAt": "2026-09-22T17:31:00+00:00",
  "sources": [
    {"source": "summary", "body": {"response":{"players":[{"personaname":"Шустрый"}]}}, "error": null},
    {"source": "friends", "body": null, "error": "HTTP 401"}
  ]
}`

// Этот тест ловит расхождение имён полей со шлюзом. Раньше его не было: фикстуры разбора
// я собрал руками в snake_case, они и проверялись — то есть формат, которого не существует.
// На проде это выглядело как «gateway answered for "", asked for 765...»: json не жалуется
// на незнакомые имена, он просто оставляет поля пустыми.
func TestClient_parsesGatewayShape(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(gatewayResponse))
	}))
	defer srv.Close()

	snapshot, err := steam.NewClient(srv.URL, nil).Fetch(context.Background(), "76561198884181842")
	if err != nil {
		t.Fatal(err)
	}

	if gotPath != "/steam/76561198884181842" {
		t.Errorf("путь запроса: got %q", gotPath)
	}
	if snapshot.SteamID != "76561198884181842" {
		t.Errorf("steamId не разобран: %q", snapshot.SteamID)
	}
	if want := time.Date(2026, 9, 22, 17, 31, 0, 0, time.UTC); !snapshot.FetchedAt.Equal(want) {
		t.Errorf("fetchedAt не разобран: got %s, want %s", snapshot.FetchedAt, want)
	}
	if len(snapshot.Sources) != 2 {
		t.Fatalf("источников: want 2, got %d", len(snapshot.Sources))
	}

	body, ok := snapshot.Body(steam.SourceSummary)
	if !ok || len(body) == 0 {
		t.Error("тело summary не разобрано")
	}
	if _, ok := snapshot.Body(steam.SourceFriends); ok {
		t.Error("friends пришёл с ошибкой и пустым телом — тела быть не должно")
	}
	if steam.ParseProfile(snapshot).PersonaName != "Шустрый" {
		t.Error("разбор сквозь клиент не дал ника")
	}
}

// Шлюз обязан отвечать про того, кого спросили. Расхождение означает ошибку на его стороне,
// и записать чужие данные под нашим id хуже, чем не записать ничего.
func TestClient_refusesAnswerAboutAnotherPlayer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"steamId":"7656119900000000","fetchedAt":"2026-09-22T17:31:00+00:00","sources":[]}`))
	}))
	defer srv.Close()

	_, err := steam.NewClient(srv.URL, nil).Fetch(context.Background(), "76561198884181842")
	if err == nil {
		t.Fatal("ответ про другого игрока должен отвергаться")
	}
}
