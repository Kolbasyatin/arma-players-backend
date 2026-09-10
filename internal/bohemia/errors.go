package bohemia

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strings"
	"syscall"
)

type ErrorKind string

const (
	KindDNSFailed         ErrorKind = "DNS_FAILED"
	KindConnectionTimeout ErrorKind = "CONNECTION_TIMEOUT"
	KindConnectionRefused ErrorKind = "CONNECTION_REFUSED"
	KindTLSError          ErrorKind = "TLS_ERROR"
	KindAuthError         ErrorKind = "AUTH_ERROR"
	KindHTTPError         ErrorKind = "HTTP_ERROR"
	KindInvalidJSON       ErrorKind = "INVALID_JSON"
	KindRoomNotFound      ErrorKind = "ROOM_NOT_FOUND" // 404 apiCode=MpRoomNotFound: roomId устарел или комнаты нет
	KindInternal          ErrorKind = "INTERNAL_ERROR"
)

// Error — любая ошибка, возвращаемая клиентом Bohemia API.
type Error struct {
	Kind       ErrorKind
	Op         string // "listPlayers", "searchRooms"
	HTTPStatus int    // 0, если ответ не получен
	Body       string // начало тела ответа при HTTP-ошибке: Bohemia пишет туда код причины
	Err        error  // исходная ошибка, может быть nil
}

func (e *Error) Error() string {
	switch {
	case e.Err != nil:
		return fmt.Sprintf("bohemia %s: %s: %v", e.Op, e.Kind, e.Err)
	case e.Body != "":
		return fmt.Sprintf("bohemia %s: %s (http %d): %s", e.Op, e.Kind, e.HTTPStatus, e.Body)
	}
	return fmt.Sprintf("bohemia %s: %s (http %d)", e.Op, e.Kind, e.HTTPStatus)
}

func (e *Error) Unwrap() error { return e.Err }

// classifyTransport превращает ошибку http.Client.Do в Error с Kind.
func classifyTransport(op string, err error) *Error {
	kind := KindInternal

	var dnsErr *net.DNSError
	var netErr net.Error
	var tlsErr *tls.CertificateVerificationError

	switch {
	case errors.Is(err, context.DeadlineExceeded):
		kind = KindConnectionTimeout
	case errors.As(err, &dnsErr):
		kind = KindDNSFailed
	case errors.Is(err, syscall.ECONNREFUSED):
		kind = KindConnectionRefused
	case errors.As(err, &tlsErr):
		kind = KindTLSError
	case errors.As(err, &netErr) && netErr.Timeout():
		kind = KindConnectionTimeout
	}

	return &Error{Kind: kind, Op: op, Err: err}
}

func classifyStatus(op string, status int, body []byte) *Error {
	kind := KindHTTPError
	switch {
	case status == 401 || status == 403:
		kind = KindAuthError
	case status == 404 && bytes.Contains(body, []byte(`"MpRoomNotFound"`)):
		// Проверено на живом API 2026-09-11: несуществующий/устаревший roomId → 404 с этим apiCode,
		// а не пустой список. Значит рестарт сервера нельзя спутать с массовым выходом игроков.
		kind = KindRoomNotFound
	}
	return &Error{Kind: kind, Op: op, HTTPStatus: status, Body: truncate(body, 512)}
}

// truncate — первые n байт тела как строка, без переводов строк.
func truncate(b []byte, n int) string {
	if len(b) > n {
		b = b[:n]
	}
	return strings.TrimSpace(strings.ReplaceAll(string(b), "\n", " "))
}
