// Package token отдаёт актуальный BI access token для запросов к Bohemia API.
//
// Сам токен добывает внешний сервис arma-reforger-hz (Steam → BI Identity); здесь только
// его получение по HTTP, кэш до истечения и инвалидация, когда Bohemia ответила 401/403
// (AGENTS §4). Отдельный пакет, чтобы tracking не знал, откуда берётся токен.
package token

import (
	"context"
	"errors"
	"sync"
	"time"
)

// Token — access token и момент его истечения.
type Token struct {
	AccessToken string
	ExpiresAt   time.Time
}

// ErrNotAvailable — источник жив, но токена у него пока нет (HTTP 404 от arma-reforger-hz).
// Для poll_run это TOKEN_UNAVAILABLE, а не сетевая ошибка.
var ErrNotAvailable = errors.New("token: not available yet")

// Source — откуда берётся токен. Реализация по умолчанию — HTTPSource.
type Source interface {
	Fetch(ctx context.Context) (Token, error)
}

// Provider выдаёт действующий токен: отдаёт закэшированный, пока до истечения больше lead, иначе запрашивает новый.
// Безопасен для конкурентного использования: один Provider делят все poll-циклы.
type Provider struct {
	src   Source
	lead  time.Duration
	now   func() time.Time // подменяется в тестах
	mu    sync.Mutex
	tok   Token
	valid bool
}

// NewProvider создаёт провайдер. lead — за сколько до истечения токен считается устаревшим
// и запрашивается заново; при 0 используется 2 минуты.
func NewProvider(src Source, lead time.Duration) *Provider {
	if lead <= 0 {
		lead = 2 * time.Minute
	}
	return &Provider{src: src, lead: lead, now: time.Now}
}

// Get возвращает действующий access token, при необходимости обновив его через Source.
func (p *Provider) Get(ctx context.Context) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.valid && p.now().Add(p.lead).Before(p.tok.ExpiresAt) {
		return p.tok.AccessToken, nil
	}

	tok, err := p.src.Fetch(ctx)
	if err != nil {
		return "", err
	}
	p.tok, p.valid = tok, true
	return tok.AccessToken, nil
}

// Invalidate сбрасывает кэш. Вызывается, когда Bohemia отвергла токен (AUTH_ERROR):
// он протух раньше срока или отозван, следующий Get пойдёт за новым.
func (p *Provider) Invalidate() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.valid = false
}

// SetClock подменяет источник времени. Нужен тестам, чтобы двигать часы без sleep.
func (p *Provider) SetClock(now func() time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.now = now
}
