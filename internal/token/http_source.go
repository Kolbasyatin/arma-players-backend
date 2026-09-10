package token

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// HTTPSource получает токен запросом GET к arma-reforger-hz (например http://arma-reforger-hz:8080/token).
type HTTPSource struct {
	url  string
	http *http.Client
}

func NewHTTPSource(url string, httpClient *http.Client) *HTTPSource {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Second}
	}
	return &HTTPSource{url: url, http: httpClient}
}

// tokenResponse — тело ответа GET /token сервиса arma-reforger-hz.
type tokenResponse struct {
	AccessToken string    `json:"accessToken"`
	ExpiresAt   time.Time `json:"expiresAt"` // RFC 3339, например 2026-09-03T15:04:05+00:00
}

func (s *HTTPSource) Fetch(ctx context.Context) (Token, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url, nil)
	if err != nil {
		return Token{}, fmt.Errorf("token: build request: %w", err)
	}

	resp, err := s.http.Do(req)
	if err != nil {
		return Token{}, fmt.Errorf("token: fetch: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return Token{}, ErrNotAvailable
	case resp.StatusCode != http.StatusOK:
		return Token{}, fmt.Errorf("token: unexpected status %d", resp.StatusCode)
	}

	var body tokenResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body); err != nil {
		return Token{}, fmt.Errorf("token: decode: %w", err)
	}
	if body.AccessToken == "" {
		return Token{}, fmt.Errorf("token: empty accessToken in response")
	}
	return Token{AccessToken: body.AccessToken, ExpiresAt: body.ExpiresAt}, nil
}
