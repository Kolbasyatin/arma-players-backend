// Package observation — учёт качества данных: каждый запрос к Bohemia фиксируется как PollRun
// с статусом из фиксированного списка (ADR 0005, AGENTS §10). Правило проекта:
// неуспешный poll ничего не говорит о состоянии сервера и игроков.
package observation

import (
	"context"
	"errors"
	"time"

	"armaplayers/internal/bohemia"
	"armaplayers/internal/token"
)

// PollType — что именно запрашивали.
type PollType string

const (
	PollLobbyScan   PollType = "LOBBY_SCAN"
	PollResolveRoom PollType = "RESOLVE_ROOM"
	PollListPlayers PollType = "LIST_PLAYERS"
)

// Status — исход запроса. SUCCESS либо вид ошибки; список закрытый, он же в poll_run.status.
type Status string

const (
	StatusSuccess           Status = "SUCCESS"
	StatusDNSFailed         Status = "DNS_FAILED"
	StatusConnectionTimeout Status = "CONNECTION_TIMEOUT"
	StatusConnectionRefused Status = "CONNECTION_REFUSED"
	StatusTLSError          Status = "TLS_ERROR"
	StatusAuthError         Status = "AUTH_ERROR"
	StatusHTTPError         Status = "HTTP_ERROR"
	StatusInvalidJSON       Status = "INVALID_JSON"
	StatusRoomNotFound      Status = "ROOM_NOT_FOUND"
	StatusTokenUnavailable  Status = "TOKEN_UNAVAILABLE"
	StatusInternalError     Status = "INTERNAL_ERROR"
)

// PollRun — одна строка журнала poll_run.
type PollRun struct {
	ServerID       *int64 // nil для полного скана лобби
	Type           PollType
	StartedAt      time.Time
	FinishedAt     time.Time
	Status         Status
	RoomID         string
	HTTPStatus     int
	ErrorMessage   string
	ConnectedCount *int
	QueueCount     *int
	DataUpdatedAt  *time.Time
}

// StatusFromError переводит ошибку любого слоя в Status. nil → SUCCESS.
func StatusFromError(err error) (Status, int) {
	if err == nil {
		return StatusSuccess, 0
	}

	var be *bohemia.Error
	if errors.As(err, &be) {
		switch be.Kind {
		case bohemia.KindDNSFailed:
			return StatusDNSFailed, be.HTTPStatus
		case bohemia.KindConnectionTimeout:
			return StatusConnectionTimeout, be.HTTPStatus
		case bohemia.KindConnectionRefused:
			return StatusConnectionRefused, be.HTTPStatus
		case bohemia.KindTLSError:
			return StatusTLSError, be.HTTPStatus
		case bohemia.KindAuthError:
			return StatusAuthError, be.HTTPStatus
		case bohemia.KindHTTPError:
			return StatusHTTPError, be.HTTPStatus
		case bohemia.KindInvalidJSON:
			return StatusInvalidJSON, be.HTTPStatus
		}
		return StatusInternalError, be.HTTPStatus
	}

	switch {
	case errors.Is(err, token.ErrNotAvailable):
		return StatusTokenUnavailable, 0
	case errors.Is(err, context.DeadlineExceeded):
		return StatusConnectionTimeout, 0
	}
	return StatusInternalError, 0
}
