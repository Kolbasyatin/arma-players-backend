package bohemia_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"armaplayers/internal/bohemia"
)

var testCfg = bohemia.Config{
	PlatformID:     "ReforgerSteam",
	GameClientType: "PLATFORM_PC",
	ClientVersion:  "1.8.0",
	UserAgent:      "test-agent",
}

// newFixtureServer поднимает сервер, который отдаёт fixture и запоминает путь и тело запроса.
func newFixtureServer(t *testing.T, fixtureFile string) (srv *httptest.Server, gotPath *string, gotBody map[string]any) {
	t.Helper()

	fixture, err := os.ReadFile("testdata/" + fixtureFile)
	if err != nil {
		t.Fatal(err)
	}

	gotPath = new(string)
	gotBody = map[string]any{}

	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*gotPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if ua := r.Header.Get("User-Agent"); ua != testCfg.UserAgent {
			t.Errorf("user-agent: got %q", ua)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(fixture)
	}))
	t.Cleanup(srv.Close)
	return srv, gotPath, gotBody
}

func newClient(srv *httptest.Server) *bohemia.Client {
	cfg := testCfg
	cfg.BaseURL = srv.URL
	return bohemia.New(cfg, srv.Client())
}

func TestClient_ListPlayers(t *testing.T) {
	srv, gotPath, gotBody := newFixtureServer(t, "list_players.json")

	resp, err := newClient(srv).ListPlayers(context.Background(), "token-123", "room-abc")
	if err != nil {
		t.Fatalf("ListPlayers: %v", err)
	}

	if *gotPath != "/lobby/rooms/listPlayers" {
		t.Errorf("path: got %s", *gotPath)
	}
	if gotBody["roomId"] != "room-abc" || gotBody["accessToken"] != "token-123" || gotBody["platformId"] != "ReforgerSteam" {
		t.Errorf("request body: %v", gotBody)
	}
	if len(resp.ConnectedPlayers) != 2 {
		t.Errorf("connected: want 2, got %d", len(resp.ConnectedPlayers))
	}
	if len(resp.Raw) == 0 {
		t.Error("raw body must be kept")
	}
}

func TestClient_SearchRooms(t *testing.T) {
	srv, gotPath, gotBody := newFixtureServer(t, "search_rooms.json")

	resp, err := newClient(srv).SearchRooms(context.Background(), "token-123", bohemia.RoomSearch{
		HostAddress: "37.48.253.41:2001",
		Limit:       5,
	})
	if err != nil {
		t.Fatalf("SearchRooms: %v", err)
	}

	if *gotPath != "/lobby/rooms/search" {
		t.Errorf("path: got %s", *gotPath)
	}

	// Контракт запроса: снят с живого клиента, без ascendent backend отвечает InvalidInput.
	want := map[string]any{
		"hostAddress":      "37.48.253.41:2001",
		"accessToken":      "token-123",
		"platformId":       "ReforgerSteam",
		"gameClientType":   "PLATFORM_PC",
		"clientVersion":    "1.8.0",
		"ascendent":        false,
		"gameClientFilter": "AnyCompatible",
		"order":            "PlayerCount",
		"lightweight":      false,
		"from":             float64(0),
		"limit":            float64(5),
	}
	for k, v := range want {
		if gotBody[k] != v {
			t.Errorf("body[%s]: want %v, got %v", k, v, gotBody[k])
		}
	}
	if pv, ok := gotBody["pingValues"].([]any); !ok || len(pv) != 0 {
		t.Errorf("pingValues: want [], got %v", gotBody["pingValues"])
	}

	// Разбор ответа.
	if resp.TotalCount != 1 || len(resp.Rooms) != 1 {
		t.Fatalf("rooms: want 1, got total=%d len=%d", resp.TotalCount, len(resp.Rooms))
	}
	room := resp.Rooms[0]
	if room.ID != "34008e3e-9a0c-4be0-b94f-c7f3cb547626" || room.HostAddress != "37.48.253.41:2001" {
		t.Errorf("room identity: %+v", room)
	}
	if room.PlayerCount != 128 || room.PlayerCountLimit != 128 || room.Updated != 1788463724 {
		t.Errorf("room counters: %+v", room)
	}
	if room.JoinQueue == nil || room.JoinQueue.Size != 7 || room.JoinQueue.MaxSize != 50 {
		t.Errorf("joinQueue: %+v", room.JoinQueue)
	}
	if room.RuntimeStats == nil || room.RuntimeStats.FPS != 59 {
		t.Errorf("runtimeStats: %+v", room.RuntimeStats)
	}
	if len(room.Mods) != 3 || room.Mods[0].ModID == "" || room.Mods[0].Name == "" {
		t.Errorf("mods: %+v", room.Mods)
	}
	if room.SessionID != "c134568a-000051ffebe3" || room.DetailsUpdatedAt != 1789050821 || !room.BattlEye || len(room.SupportedGameClientTypes) != 3 {
		t.Errorf("room details: %+v", room)
	}
}

func TestClient_SearchRooms_defaultLimit(t *testing.T) {
	srv, _, gotBody := newFixtureServer(t, "search_rooms.json")

	if _, err := newClient(srv).SearchRooms(context.Background(), "t", bohemia.RoomSearch{}); err != nil {
		t.Fatal(err)
	}
	if gotBody["limit"] != float64(50) {
		t.Errorf("default limit: got %v", gotBody["limit"])
	}
}

func TestClient_errors(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		body     string
		wantKind bohemia.ErrorKind
	}{
		{"unauthorized", 401, `{}`, bohemia.KindAuthError},
		{"forbidden", 403, `{}`, bohemia.KindAuthError},
		{"server error", 500, `oops`, bohemia.KindHTTPError},
		{"invalid json", 200, `{not json`, bohemia.KindInvalidJSON},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			client := newClient(srv)

			_, err := client.ListPlayers(context.Background(), "t", "r")
			assertKind(t, err, tc.wantKind)

			_, err = client.SearchRooms(context.Background(), "t", bohemia.RoomSearch{})
			assertKind(t, err, tc.wantKind)
		})
	}
}

func TestClient_connectionRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // порт освобождён → connection refused

	_, err := newClient(srv).ListPlayers(context.Background(), "t", "r")
	assertKind(t, err, bohemia.KindConnectionRefused)
}

func TestClient_timeout(t *testing.T) {
	// release — канал, закрытием которого тест отпускает зависший обработчик.
	// Ждать r.Context().Done() нельзя: тело POST не прочитано, и сервер не заметит обрыв соединения.
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, err := newClient(srv).ListPlayers(ctx, "t", "r")
	close(release)

	assertKind(t, err, bohemia.KindConnectionTimeout)
}

// assertKind проверяет, что err — *bohemia.Error с ожидаемым Kind.
func assertKind(t *testing.T, err error, want bohemia.ErrorKind) {
	t.Helper()

	var be *bohemia.Error
	if !errors.As(err, &be) {
		t.Fatalf("want *bohemia.Error, got %T: %v", err, err)
	}
	if be.Kind != want {
		t.Errorf("kind: want %s, got %s", want, be.Kind)
	}
}

func TestClient_SearchAllRooms(t *testing.T) {
	// 7 комнат, страница 3 → ожидаем страницы 3,3,1 и запросы from=0,3,6.
	const total, pageSize = 7, 3
	var froms []int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			From  int `json:"from"`
			Limit int `json:"limit"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode: %v", err)
		}
		froms = append(froms, body.From)

		rooms := make([]map[string]any, 0, body.Limit)
		for i := body.From; i < total && i < body.From+body.Limit; i++ {
			rooms = append(rooms, map[string]any{"id": fmt.Sprintf("room-%d", i), "hostAddress": fmt.Sprintf("10.0.0.%d:2001", i)})
		}
		json.NewEncoder(w).Encode(map[string]any{"rooms": rooms, "searchFrom": body.From, "totalCount": total})
	}))
	defer srv.Close()

	var got []string
	err := newClient(srv).SearchAllRooms(context.Background(), "t", pageSize, func(page bohemia.SearchRoomsResponse) error {
		for _, r := range page.Rooms {
			got = append(got, r.ID)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != total || got[0] != "room-0" || got[6] != "room-6" {
		t.Errorf("rooms: %v", got)
	}
	if fmt.Sprint(froms) != "[0 3 6]" {
		t.Errorf("froms: %v", froms)
	}
}
