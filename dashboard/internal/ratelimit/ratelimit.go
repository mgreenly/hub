// Package ratelimit is the per-token sliding-window limiter for dashboard.
//
// The ledger is keyed by the server-side `oauth_tokens.id` — never the
// plaintext or its hash — so credential material does not enter the
// limiter. The ledger is in-memory and per-instance; this box ships a
// single instance and multi-instance deploys are out of scope.
package ratelimit

import (
	"sync"
	"time"
)

// Limiter is a sliding-window rate limiter. Decide returns allowed=true
// while the per-key request count over the trailing window is below the
// configured ceiling; on rejection it also returns the running count for
// the current window so callers can group audit rows per window.
type Limiter struct {
	limit  int
	window time.Duration
	now    func() time.Time
	mu     sync.Mutex
	hits   map[string][]time.Time
}

// New constructs a Limiter. A non-positive limit or window disables
// limiting (Decide returns allowed=true unconditionally) — useful for
// tests and for an operator who wants to opt out.
func New(limit int, window time.Duration) *Limiter {
	return &Limiter{
		limit:  limit,
		window: window,
		now:    time.Now,
		hits:   make(map[string][]time.Time),
	}
}

// Decision is returned by Decide. WindowCount is the number of requests
// observed for this key within the current trailing window, including
// the just-evaluated request when Allowed is true.
type Decision struct {
	Allowed     bool
	WindowCount int
	WindowStart time.Time
}

// Decide records a request against key and reports whether it is
// allowed. When key is empty (e.g. an unauthenticated caller that
// somehow reached the limiter — should not happen given upstream
// ordering), Decide allows the request and does not record it.
func (l *Limiter) Decide(key string) Decision {
	if l == nil || l.limit <= 0 || l.window <= 0 || key == "" {
		return Decision{Allowed: true}
	}
	now := l.now().UTC()
	cutoff := now.Add(-l.window)
	l.mu.Lock()
	defer l.mu.Unlock()
	hits := l.hits[key]
	// Drop stale entries.
	i := 0
	for ; i < len(hits); i++ {
		if hits[i].After(cutoff) {
			break
		}
	}
	if i > 0 {
		hits = hits[i:]
	}
	if len(hits) >= l.limit {
		l.hits[key] = hits
		return Decision{Allowed: false, WindowCount: len(hits), WindowStart: cutoff}
	}
	hits = append(hits, now)
	l.hits[key] = hits
	return Decision{Allowed: true, WindowCount: len(hits), WindowStart: cutoff}
}

// Limit returns the configured per-window ceiling.
func (l *Limiter) Limit() int { return l.limit }

// Window returns the configured trailing window.
func (l *Limiter) Window() time.Duration { return l.window }
