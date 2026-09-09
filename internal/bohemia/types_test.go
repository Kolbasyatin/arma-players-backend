package bohemia_test

import (
	"encoding/json"
	"os"
	"testing"

	"armaplayers/internal/bohemia"
)

func TestListPlayersResponse_Decode(t *testing.T) {
	raw, err := os.ReadFile("testdata/list_players.json")
	if err != nil {
		t.Fatal(err)
	}

	var resp bohemia.ListPlayersResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(resp.ConnectedPlayers) != 2 {
		t.Fatalf("connected: want 2, got %d", len(resp.ConnectedPlayers))
	}
	if got := resp.ConnectedPlayers[0].PlatformUserID; got != "76561198884181842" {
		t.Errorf("platformUserId: got %q", got)
	}
	if len(resp.QueuePlayers) != 0 {
		t.Errorf("queue: want empty, got %d", len(resp.QueuePlayers))
	}
}
