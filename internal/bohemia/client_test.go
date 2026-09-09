package bohemia_test

import (
	"armaplayers/internal/bohemia"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestClient_ListPlayers(t *testing.T) {
	fixture, err := os.ReadFile("testdata/list_players.json")
	if err != nil {
		t.Fatal(err)
	}

	var gotBody map[string]string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/lobby/rooms/listPlayers" {
			t.Errorf("path: got %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(fixture)
	}))
	defer srv.Close()

	client := bohemia.New(bohemia.Config{
		BaseURL:       srv.URL,
		PlatformID:    "ReforgerSteam",
		ClientVersion: "1.8.0",
		UserAgent:     "test-agent",
	}, srv.Client())

	resp, err := client.ListPlayers(context.Background(), "token-123", "room-abc")
	if err != nil {
		t.Fatalf("ListPlayers: %v", err)
	}

	if len(resp.ConnectedPlayers) != 2 {
		t.Errorf("connected: want 2, got %d", len(resp.ConnectedPlayers))
	}
	if gotBody["roomId"] != "room-abc" || gotBody["accessToken"] != "token-123" || gotBody["platformId"] != "ReforgerSteam" {
		t.Errorf("request body: %v", gotBody)
	}

}

func TestClient_ListPlayers_errors(t *testing.T) {
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

			client := bohemia.New(bohemia.Config{BaseURL: srv.URL}, srv.Client())
			_, err := client.ListPlayers(context.Background(), "t", "r")

			var be *bohemia.Error
			if !errors.As(err, &be) {
				t.Fatalf("want *bohemia.Error, got %T: %v", err, err)
			}
			if be.Kind != tc.wantKind {
				t.Errorf("kind: want %s, got %s", tc.wantKind, be.Kind)
			}
		})
	}
}

func TestClient_ListPlayers_connectionRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // сервер закрыт, порт свободен → connection refused

	client := bohemia.New(bohemia.Config{BaseURL: srv.URL}, nil)
	_, err := client.ListPlayers(context.Background(), "t", "r")

	var be *bohemia.Error
	if !errors.As(err, &be) || be.Kind != bohemia.KindConnectionRefused {
		t.Fatalf("want CONNECTION_REFUSED, got %v", err)
	}
}
