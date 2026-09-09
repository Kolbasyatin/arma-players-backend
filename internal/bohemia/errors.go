package bohemia

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
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
	KindInternal          ErrorKind = "INTERNAL_ERROR"
)

// Error — любая ошибка, возвращаемая клиентом Bohemia API.
type Error struct {
	Kind       ErrorKind
	Op         string // "listPlayers", "searchRooms"
	HTTPStatus int    // 0, если ответ не получен
	Err        error  // исходная ошибка, может быть nil
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("bohemia %s: %s: %v", e.Op, e.Kind, e.Err)
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

func classifyStatus(op string, status int) *Error {
	kind := KindHTTPError
	if status == 401 || status == 403 {
		kind = KindAuthError
	}
	return &Error{Kind: kind, Op: op, HTTPStatus: status}
}
