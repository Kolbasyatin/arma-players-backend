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
	if len(resp.QueuePlayers) != 2 {
		t.Fatalf("queue: want 2, got %d", len(resp.QueuePlayers))
	}
	// Не-Steam платформы: platformUserId другого формата, должен сохраняться как есть.
	if got := resp.QueuePlayers[1]; got.GameClientType != "PLATFORM_XBL" || got.PlatformUserID != "4377A60943B8B0397988DDE201F8A58A12B12647" {
		t.Errorf("xbl player: %+v", got)
	}
}
