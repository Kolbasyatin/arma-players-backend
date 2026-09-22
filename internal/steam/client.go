package steam

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// Client — шлюз arma-reforger-hz. Ключ Valve знает только он; мы ходим по внутренней сети.
type Client struct {
	baseURL string
	http    *http.Client
}

// NewClient. baseURL — корень сервиса, например http://arma-reforger-hz:8080.
func NewClient(baseURL string, httpClient *http.Client) *Client {
	if httpClient == nil {
		// Таймаут заметно больше, чем у обычного запроса: шлюз внутри ходит в Valve
		// за четырьмя источниками сразу, и медленный ответ там — нормальное явление.
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{baseURL: baseURL, http: httpClient}
}

// Fetch забирает снимок по SteamID64.
func (c *Client) Fetch(ctx context.Context, steamID string) (Snapshot, error) {
	var snapshot Snapshot

	endpoint, err := url.JoinPath(c.baseURL, "steam", steamID)
	if err != nil {
		return snapshot, fmt.Errorf("steam: build url: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return snapshot, fmt.Errorf("steam: request: %w", err)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return snapshot, fmt.Errorf("steam: fetch %s: %w", steamID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return snapshot, fmt.Errorf("steam: gateway returned %d for %s", resp.StatusCode, steamID)
	}

	if err := json.NewDecoder(resp.Body).Decode(&snapshot); err != nil {
		return snapshot, fmt.Errorf("steam: decode %s: %w", steamID, err)
	}

	// Шлюз обязан вернуть того, кого спросили. Расхождение означает ошибку на его стороне,
	// и записать чужие данные под нашим id хуже, чем не записать ничего.
	if snapshot.SteamID != steamID {
		return snapshot, fmt.Errorf("steam: gateway answered for %q, asked for %q", snapshot.SteamID, steamID)
	}

	return snapshot, nil
}
