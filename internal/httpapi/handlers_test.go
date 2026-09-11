package httpapi_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"armaplayers/internal/httpapi"
)

type fakeStore struct {
	events  []httpapi.Event
	lastQ   httpapi.EventsQuery
	players []httpapi.PlayerSummary
}

func (f *fakeStore) Events(_ context.Context, q httpapi.EventsQuery) ([]httpapi.Event, error) {
	f.lastQ = q
	var out []httpapi.Event
	for _, e := range f.events {
		if e.ID > q.After {
			out = append(out, e)
		}
	}
	return out, nil
}
func (f *fakeStore) EventsHead(context.Context) (int64, error) { return 42, nil }
func (f *fakeStore) SearchPlayers(context.Context, string, int) ([]httpapi.PlayerSummary, error) {
	return f.players, nil
}
func (f *fakeStore) Player(_ context.Context, id int64) (httpapi.PlayerSummary, bool, error) {
	for _, p := range f.players {
		if p.PlayerID == id {
			return p, true, nil
		}
	}
	return httpapi.PlayerSummary{}, false, nil
}
func (f *fakeStore) PlayerSessions(context.Context, int64, int) ([]httpapi.Session, error) {
	return nil, nil
}
func (f *fakeStore) Servers(context.Context, bool, string) ([]httpapi.ServerSummary, error) {
	return nil, nil
}

func newAPI(t *testing.T, store *fakeStore) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(httpapi.NewServer(":0", fakeStatus{}, store, "secret").Handler)
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, url, token string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var m map[string]any
	_ = json.Unmarshal(body, &m)
	return resp.StatusCode, m
}

func TestAPI_auth(t *testing.T) {
	srv := newAPI(t, &fakeStore{})
	if code, _ := get(t, srv.URL+"/events", ""); code != http.StatusUnauthorized {
		t.Errorf("no token: want 401, got %d", code)
	}
	if code, _ := get(t, srv.URL+"/events", "wrong"); code != http.StatusUnauthorized {
		t.Errorf("wrong token: want 401, got %d", code)
	}
	if code, _ := get(t, srv.URL+"/events", "secret"); code != http.StatusOK {
		t.Errorf("right token: want 200, got %d", code)
	}
	if code, _ := get(t, srv.URL+"/health", ""); code != http.StatusOK {
		t.Errorf("health must stay open, got %d", code)
	}
}

func TestAPI_disabledWithoutToken(t *testing.T) {
	srv := httptest.NewServer(httpapi.NewServer(":0", fakeStatus{}, &fakeStore{}, "").Handler)
	defer srv.Close()
	if code, _ := get(t, srv.URL+"/events", "anything"); code != http.StatusServiceUnavailable {
		t.Errorf("want 503 when API_TOKEN empty, got %d", code)
	}
}

func TestAPI_eventsCursorAndFilters(t *testing.T) {
	store := &fakeStore{events: []httpapi.Event{{ID: 10, Type: "PLAYER_JOINED_SERVER"}, {ID: 11, Type: "PLAYER_LEFT_SERVER"}, {ID: 12, Type: "PLAYER_JOINED_SERVER"}}}
	srv := newAPI(t, store)

	code, body := get(t, srv.URL+"/events?after=10&player_ids=5,7&types=PLAYER_JOINED_SERVER&limit=50", "secret")
	if code != 200 {
		t.Fatalf("code %d", code)
	}
	if body["next_after"].(float64) != 12 || len(body["events"].([]any)) != 2 {
		t.Errorf("body: %v", body)
	}
	q := store.lastQ
	if q.After != 10 || len(q.PlayerIDs) != 2 || q.PlayerIDs[1] != 7 || q.Types[0] != "PLAYER_JOINED_SERVER" || q.Limit != 50 || q.IncludeReplay {
		t.Errorf("query passed to store: %+v", q)
	}

	// Нет новых событий: next_after равен переданному after, events — пустой массив, не null.
	code, body = get(t, srv.URL+"/events?after=12", "secret")
	if code != 200 || body["next_after"].(float64) != 12 || len(body["events"].([]any)) != 0 {
		t.Errorf("empty page: %d %v", code, body)
	}

	if code, _ := get(t, srv.URL+"/events?after=abc", "secret"); code != http.StatusBadRequest {
		t.Errorf("bad after: want 400, got %d", code)
	}
	if code, body := get(t, srv.URL+"/events/head", "secret"); code != 200 || body["head"].(float64) != 42 {
		t.Errorf("head: %d %v", code, body)
	}
}

func TestAPI_players(t *testing.T) {
	now := time.Now().UTC()
	store := &fakeStore{players: []httpapi.PlayerSummary{{PlayerID: 4812, BohemiaUserID: "u", CurrentNickname: "Salat", Aliases: []string{"Salat"}, FirstSeenAt: now, LastSeenAt: now}}}
	srv := newAPI(t, store)

	if code, _ := get(t, srv.URL+"/players?nick=s", "secret"); code != http.StatusBadRequest {
		t.Errorf("short nick: want 400, got %d", code)
	}
	code, body := get(t, srv.URL+"/players?nick=sal", "secret")
	if code != 200 || len(body["players"].([]any)) != 1 {
		t.Errorf("search: %d %v", code, body)
	}
	code, body = get(t, srv.URL+"/players/4812", "secret")
	if code != 200 || body["current_nickname"] != "Salat" {
		t.Errorf("player: %d %v", code, body)
	}
	if code, _ := get(t, srv.URL+"/players/999", "secret"); code != http.StatusNotFound {
		t.Errorf("missing player: want 404, got %d", code)
	}
	code, body = get(t, srv.URL+"/players/4812/sessions", "secret")
	if code != 200 || !strings.Contains(mustJSON(body), `"sessions":[]`) {
		t.Errorf("sessions must be [] not null: %d %s", code, mustJSON(body))
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
