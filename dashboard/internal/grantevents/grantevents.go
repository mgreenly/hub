// Package grantevents is the in-process pub/sub the index page's live-grants
// block uses to stay fresh. Publishers (token issuance, refresh, revocation,
// reuse cascade, and the web revoke) call Publish with the chain owner's
// email; subscribers — one per open SSE connection — receive a notify on their
// channel. The bus is intentionally minimal: a single event type ("chains")
// and a non-blocking publish that drops if the subscriber's buffer is full
// (the SSE handler re-renders from the database on every notify, so a dropped
// redundant event is harmless).
package grantevents

import "sync"

// Bus fans out grant-change notifications keyed by owner_email.
type Bus struct {
	mu   sync.Mutex
	next int
	subs map[string]map[int]chan struct{}
}

// New returns a Bus ready to use.
func New() *Bus {
	return &Bus{subs: make(map[string]map[int]chan struct{})}
}

// Subscribe registers a subscriber for owner. The returned cancel removes the
// subscription; callers must call it when done.
func (b *Bus) Subscribe(owner string) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	b.mu.Lock()
	id := b.next
	b.next++
	if b.subs[owner] == nil {
		b.subs[owner] = make(map[int]chan struct{})
	}
	b.subs[owner][id] = ch
	b.mu.Unlock()
	cancel := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if m, ok := b.subs[owner]; ok {
			delete(m, id)
			if len(m) == 0 {
				delete(b.subs, owner)
			}
		}
	}
	return ch, cancel
}

// Publish wakes every subscriber registered for owner. Empty owner is a no-op
// (publishers occasionally have only a client_id). Non-blocking — a subscriber
// whose buffer is already full just keeps the pending notify it already has,
// which is functionally identical (the SSE handler re-renders from current DB
// state). A nil Bus is also a no-op so call sites need not guard.
func (b *Bus) Publish(owner string) {
	if b == nil || owner == "" {
		return
	}
	b.mu.Lock()
	subs := b.subs[owner]
	chs := make([]chan struct{}, 0, len(subs))
	for _, ch := range subs {
		chs = append(chs, ch)
	}
	b.mu.Unlock()
	for _, ch := range chs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
