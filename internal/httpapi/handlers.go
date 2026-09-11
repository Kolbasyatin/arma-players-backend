package httpapi

import (
	"crypto/subtle"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// registerAPI подключает защищённые токеном маршруты.
func registerAPI(mux *http.ServeMux, store Store, token string) {
	auth := bearerAuth(token)

	mux.Handle("GET /events", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q, err := parseEventsQuery(r)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody(err.Error()))
			return
		}
		events, err := store.Events(r.Context(), q)
		if err != nil {
			serverError(w, "events", err)
			return
		}
		var next int64 = q.After
		if len(events) > 0 {
			next = events[len(events)-1].ID
		}
		writeJSON(w, http.StatusOK, map[string]any{"events": nonNil(events), "next_after": next})
	})))

	mux.Handle("GET /events/head", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		head, err := store.EventsHead(r.Context())
		if err != nil {
			serverError(w, "events head", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"head": head})
	})))

	mux.Handle("GET /players", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nick := strings.TrimSpace(r.URL.Query().Get("nick"))
		if len(nick) < 2 {
			writeJSON(w, http.StatusBadRequest, errorBody("nick: at least 2 characters"))
			return
		}
		limit := intParam(r, "limit", 20, 1, 100)
		players, err := store.SearchPlayers(r.Context(), nick, limit)
		if err != nil {
			serverError(w, "search players", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"players": nonNil(players)})
	})))

	mux.Handle("GET /players/{id}", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody("id: integer expected"))
			return
		}
		p, found, err := store.Player(r.Context(), id)
		if err != nil {
			serverError(w, "player", err)
			return
		}
		if !found {
			writeJSON(w, http.StatusNotFound, errorBody("player not found"))
			return
		}
		writeJSON(w, http.StatusOK, p)
	})))

	mux.Handle("GET /players/{id}/sessions", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorBody("id: integer expected"))
			return
		}
		sessions, err := store.PlayerSessions(r.Context(), id, intParam(r, "limit", 50, 1, 500))
		if err != nil {
			serverError(w, "player sessions", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"sessions": nonNil(sessions)})
	})))

	mux.Handle("GET /servers", auth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tracked := r.URL.Query().Get("tracked") == "true"
		servers, err := store.Servers(r.Context(), tracked, strings.TrimSpace(r.URL.Query().Get("name")))
		if err != nil {
			serverError(w, "servers", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"servers": nonNil(servers)})
	})))
}

// bearerAuth — middleware: Authorization: Bearer <token>. Пустой token отключает API целиком (503).
func bearerAuth(token string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if token == "" {
				writeJSON(w, http.StatusServiceUnavailable, errorBody("API disabled: API_TOKEN is not configured"))
				return
			}
			got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			// Сравнение за постоянное время, чтобы по задержке нельзя было подбирать токен.
			if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				w.Header().Set("WWW-Authenticate", "Bearer")
				writeJSON(w, http.StatusUnauthorized, errorBody("unauthorized"))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func parseEventsQuery(r *http.Request) (EventsQuery, error) {
	qs := r.URL.Query()
	q := EventsQuery{Limit: intParam(r, "limit", 200, 1, 1000), IncludeReplay: qs.Get("include_replay") == "true"}

	if v := qs.Get("after"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			return q, errors.New("after: non-negative integer expected")
		}
		q.After = n
	}
	if v := qs.Get("player_ids"); v != "" {
		for _, part := range strings.Split(v, ",") {
			n, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
			if err != nil {
				return q, errors.New("player_ids: comma-separated integers expected")
			}
			q.PlayerIDs = append(q.PlayerIDs, n)
		}
		if len(q.PlayerIDs) > 1000 {
			return q, errors.New("player_ids: at most 1000")
		}
	}
	if v := qs.Get("types"); v != "" {
		for _, t := range strings.Split(v, ",") {
			q.Types = append(q.Types, strings.TrimSpace(t))
		}
	}
	return q, nil
}

func intParam(r *http.Request, name string, def, min, max int) int {
	v := r.URL.Query().Get(name)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < min {
		return def
	}
	if n > max {
		return max
	}
	return n
}

func errorBody(msg string) map[string]string { return map[string]string{"error": msg} }

func serverError(w http.ResponseWriter, op string, err error) {
	logger().Error("api: "+op, "err", err)
	writeJSON(w, http.StatusInternalServerError, errorBody("internal error"))
}

// nonNil — пустой слайс вместо null в JSON, чтобы клиентам не проверять оба варианта.
func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
