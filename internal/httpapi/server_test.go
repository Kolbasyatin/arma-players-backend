package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"armaplayers/internal/httpapi"
)

type fakeStatus struct{ st httpapi.Status }

func (f fakeStatus) Status(context.Context) (httpapi.Status, error) { return f.st, nil }

func TestServer_endpoints(t *testing.T) {
	now := time.Now().UTC()
	srv := httptest.NewServer(httpapi.NewServer(":0", fakeStatus{st: httpapi.Status{Now: now, ServersTracked: 3, PollsLastHour: map[string]int{"SUCCESS": 10}}}, nil, "").Handler)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/health")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("health: %v %v", err, resp)
	}
	resp.Body.Close()

	resp, err = http.Get(srv.URL + "/observation-status")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("status: %v %v", err, resp)
	}
	defer resp.Body.Close()
	var got httpapi.Status
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatal(err)
	}
	if got.ServersTracked != 3 || got.PollsLastHour["SUCCESS"] != 10 {
		t.Errorf("status body: %+v", got)
	}

	if resp, _ := http.Post(srv.URL+"/health", "", nil); resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST /health: want 405, got %d", resp.StatusCode)
	}
}
