package ratelimit

import (
	"testing"
	"time"
)

// R-K5RY-83NL: sliding-window limiter keyed by oauth_tokens.id.
func TestLimiter_AllowThenReject(t *testing.T) {
	l := New(3, 10*time.Second)
	now := time.Unix(1_700_000_000, 0).UTC()
	l.now = func() time.Time { return now }
	key := "tok-id-1"
	for i := 0; i < 3; i++ {
		d := l.Decide(key)
		if !d.Allowed {
			t.Fatalf("request %d should be allowed; got %+v", i, d)
		}
	}
	d := l.Decide(key)
	if d.Allowed {
		t.Fatalf("4th request should be rejected; got %+v", d)
	}
	if d.WindowCount != 3 {
		t.Fatalf("WindowCount=%d, want 3", d.WindowCount)
	}
}

// Window slide releases capacity.
func TestLimiter_WindowSlide(t *testing.T) {
	l := New(2, 10*time.Second)
	t0 := time.Unix(1_700_000_000, 0).UTC()
	l.now = func() time.Time { return t0 }
	key := "k"
	l.Decide(key)
	l.Decide(key)
	if d := l.Decide(key); d.Allowed {
		t.Fatalf("expected reject")
	}
	// Move clock past window.
	l.now = func() time.Time { return t0.Add(11 * time.Second) }
	if d := l.Decide(key); !d.Allowed {
		t.Fatalf("expected allow after slide; got %+v", d)
	}
}

// Distinct keys are independent.
func TestLimiter_KeyIsolation(t *testing.T) {
	l := New(1, 10*time.Second)
	l.now = func() time.Time { return time.Unix(1_700_000_000, 0).UTC() }
	if !l.Decide("a").Allowed {
		t.Fatal("a/1 should pass")
	}
	if l.Decide("a").Allowed {
		t.Fatal("a/2 should reject")
	}
	if !l.Decide("b").Allowed {
		t.Fatal("b/1 should pass")
	}
}

// nil/zero limiter is permissive (test-friendly).
func TestLimiter_Disabled(t *testing.T) {
	if !(*Limiter)(nil).Decide("k").Allowed {
		t.Fatal("nil limiter must allow")
	}
	l := New(0, 0)
	if !l.Decide("k").Allowed {
		t.Fatal("zero-limit must allow")
	}
}

// Credential material must never appear as a key (R-K5RY-83NL). The
// limiter does not enforce this — that contract lives at the call site
// — but the public API only takes a string and the empty-key shortcut
// is a defensive backstop.
func TestLimiter_EmptyKey(t *testing.T) {
	l := New(1, time.Second)
	if !l.Decide("").Allowed {
		t.Fatal("empty key must allow without recording")
	}
	if !l.Decide("").Allowed {
		t.Fatal("empty key second call must allow")
	}
}
