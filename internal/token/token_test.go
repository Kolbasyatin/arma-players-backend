package token_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"armaplayers/internal/token"
)

// fakeSource — Source с настраиваемым ответом и счётчиком вызовов.
type fakeSource struct {
	tok   token.Token
	err   error
	calls atomic.Int32
}

func (f *fakeSource) Fetch(context.Context) (token.Token, error) {
	f.calls.Add(1)
	return f.tok, f.err
}

func TestCache_reusesUntilLead(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	src := &fakeSource{tok: token.Token{AccessToken: "t1", ExpiresAt: now.Add(time.Hour)}}

	c := token.NewProvider(src, 5*time.Minute)
	c.SetClock(func() time.Time { return now })

	for i := 0; i < 3; i++ {
		got, err := c.Get(context.Background())
		if err != nil || got != "t1" {
			t.Fatalf("Get #%d: %q, %v", i, got, err)
		}
	}
	if n := src.calls.Load(); n != 1 {
		t.Errorf("source calls: want 1, got %d", n)
	}

	// До истечения 4 минуты — меньше lead, кэш устарел → новый запрос.
	now = now.Add(56 * time.Minute)
	src.tok.AccessToken = "t2"
	src.tok.ExpiresAt = now.Add(time.Hour)

	got, err := c.Get(context.Background())
	if err != nil || got != "t2" {
		t.Fatalf("Get after expiry: %q, %v", got, err)
	}
	if n := src.calls.Load(); n != 2 {
		t.Errorf("source calls: want 2, got %d", n)
	}
}

func TestCache_invalidateForcesRefetch(t *testing.T) {
	src := &fakeSource{tok: token.Token{AccessToken: "t1", ExpiresAt: time.Now().Add(time.Hour)}}
	c := token.NewProvider(src, 0)

	if _, err := c.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	c.Invalidate()
	if _, err := c.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := src.calls.Load(); n != 2 {
		t.Errorf("source calls: want 2, got %d", n)
	}
}

func TestCache_sourceErrorPropagates(t *testing.T) {
	src := &fakeSource{err: token.ErrNotAvailable}
	c := token.NewProvider(src, 0)

	_, err := c.Get(context.Background())
	if !errors.Is(err, token.ErrNotAvailable) {
		t.Fatalf("want ErrNotAvailable, got %v", err)
	}
}

func TestHTTPSource_Fetch(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantTok string
		wantErr error // nil — любая ошибка не ожидается; ErrNotAvailable — именно она
		anyErr  bool
	}{
		{
			name:    "ok",
			status:  200,
			body:    `{"accessToken":"jwt-1","expiresAt":"2026-09-03T15:04:05+00:00"}`,
			wantTok: "jwt-1",
		},
		{name: "not available", status: 404, wantErr: token.ErrNotAvailable},
		{name: "server error", status: 500, anyErr: true},
		{name: "bad json", status: 200, body: `{oops`, anyErr: true},
		{name: "empty token", status: 200, body: `{"accessToken":"","expiresAt":"2026-09-03T15:04:05+00:00"}`, anyErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/token" {
					t.Errorf("request: %s %s", r.Method, r.URL.Path)
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			got, err := token.NewHTTPSource(srv.URL+"/token", srv.Client()).Fetch(context.Background())

			switch {
			case tc.wantErr != nil:
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("want %v, got %v", tc.wantErr, err)
				}
			case tc.anyErr:
				if err == nil {
					t.Fatal("want error, got nil")
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
				if got.AccessToken != tc.wantTok {
					t.Errorf("token: want %q, got %q", tc.wantTok, got.AccessToken)
				}
				want := time.Date(2026, 9, 3, 15, 4, 5, 0, time.UTC)
				if !got.ExpiresAt.Equal(want) {
					t.Errorf("expiresAt: want %v, got %v", want, got.ExpiresAt)
				}
			}
		})
	}
}

// TestProvider_concurrentGet — 10 горутин одновременно зовут Get. Проверяется под go test -race:
// без мьютекса детектор гонок покажет одновременную запись и чтение p.tok / p.valid.
func TestProvider_concurrentGet(t *testing.T) {
	src := &fakeSource{tok: token.Token{AccessToken: "t1", ExpiresAt: time.Now().Add(time.Hour)}}
	p := token.NewProvider(src, 0)

	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				if _, err := p.Get(context.Background()); err != nil {
					t.Error(err)
					return
				}
				if j%10 == 0 {
					p.Invalidate()
				}
			}
		}()
	}
	wg.Wait()
}
