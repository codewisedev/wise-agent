package auth

import (
	"sync"
	"time"
)

// LoginLimiter is a fixed-window failure counter keyed by client IP.
type LoginLimiter struct {
	mu      sync.Mutex
	max     int
	window  time.Duration
	entries map[string]*limitEntry
	now     func() time.Time
}

type limitEntry struct {
	failures int
	start    time.Time
}

func NewLoginLimiter(max int, window time.Duration) *LoginLimiter {
	return &LoginLimiter{max: max, window: window, entries: map[string]*limitEntry{}, now: time.Now}
}

// Allowed reports whether key may attempt another login.
func (l *LoginLimiter) Allowed(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.entries[key]
	if !ok {
		return true
	}
	if l.now().Sub(e.start) > l.window {
		delete(l.entries, key)
		return true
	}
	return e.failures < l.max
}

// Fail records a failed attempt.
func (l *LoginLimiter) Fail(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	e, ok := l.entries[key]
	if !ok || now.Sub(e.start) > l.window {
		e = &limitEntry{start: now}
		l.entries[key] = e
	}
	e.failures++
	if len(l.entries) > 10_000 { // bound memory
		for k, v := range l.entries {
			if now.Sub(v.start) > l.window {
				delete(l.entries, k)
			}
		}
	}
}

// Reset clears failures after a successful login.
func (l *LoginLimiter) Reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.entries, key)
}
