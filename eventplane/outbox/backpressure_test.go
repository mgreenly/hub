package outbox

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"
)

// blockingRW is a ResponseWriter whose every Write blocks until the test
// releases it, and which counts only COMPLETED writes. It lets us prove the
// producer's write() blocks on a slow reader rather than buffering the backlog.
type blockingRW struct {
	header    http.Header
	allow     chan struct{} // each Write waits for one token before completing
	completed atomic.Int64  // frames fully written
}

func newBlockingRW() *blockingRW {
	return &blockingRW{header: http.Header{}, allow: make(chan struct{})}
}

func (b *blockingRW) Header() http.Header { return b.header }
func (b *blockingRW) WriteHeader(int)     {}
func (b *blockingRW) Flush()              {}
func (b *blockingRW) Write(p []byte) (int, error) {
	<-b.allow // block until the test grants this write
	b.completed.Add(1)
	return len(p), nil
}

// TestBackpressure_WriteBlocksBacklogStaysOnDisk is the §5.3 slow-reader test:
// against a stalled reader and a large backlog, the producer's write() blocks
// (it does not race ahead buffering frames), and the whole backlog stays on
// disk. Each released write admits exactly one more frame, demonstrating
// frame-by-frame, bounded streaming.
func TestBackpressure_WriteBlocksBacklogStaysOnDisk(t *testing.T) {
	o, db := newMemOutbox(t)
	const backlog = 25
	for i := 0; i < backlog; i++ {
		appendOne(t, o, db, "contact.created")
	}

	rw := newBlockingRW()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "/feed", nil)

	done := make(chan struct{})
	go func() {
		o.FeedHandler().ServeHTTP(rw, req)
		close(done)
	}()

	// With the reader stalled, the producer must be blocked inside Write with
	// nothing completed and the entire backlog still on disk.
	time.Sleep(50 * time.Millisecond)
	if n := rw.completed.Load(); n != 0 {
		t.Fatalf("producer wrote %d frames to a stalled reader; write() must block", n)
	}
	if got := countRows(t, o); got != backlog {
		t.Fatalf("backlog must stay on disk: got %d want %d", got, backlog)
	}

	// Release writes one at a time; each admits exactly one more completed frame.
	for i := int64(1); i <= 5; i++ {
		rw.allow <- struct{}{}
		// Give the goroutine a moment to complete that single write.
		deadline := time.Now().Add(time.Second)
		for rw.completed.Load() < i && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if got := rw.completed.Load(); got != i {
			t.Fatalf("after releasing %d writes, completed=%d (want %d) — not frame-by-frame", i, got, i)
		}
	}

	// Backlog is still fully on disk; nothing was deleted by streaming.
	if got := countRows(t, o); got != backlog {
		t.Fatalf("streaming must not delete: got %d want %d", got, backlog)
	}

	// Drain the rest so the handler can exit when ctx is cancelled.
	cancel()
	go func() {
		for {
			select {
			case rw.allow <- struct{}{}:
			case <-done:
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not exit after cancel")
	}
}
