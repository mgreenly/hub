package outbox

import (
	"context"
	"testing"
	"time"
)

func countRows(t *testing.T, o *Outbox) int {
	t.Helper()
	var n int
	if err := o.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM outbox`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}

// TestTrim_RowBackstopOnlyTrimsBeyondBothHorizons verifies the conservative
// floor (§11.3): with rows that are ALSO older than the time horizon, the row
// backstop trims down to keeping retentionRows newest rows.
func TestTrim_RowBackstopOnlyTrimsBeyondBothHorizons(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	o, db := newMemOutbox(t, func(opt *Options) {
		opt.RetentionMaxRows = 5
		opt.RetentionDays = 7
		opt.Now = func() time.Time { return base } // append timestamps in the far past
	})
	for i := 0; i < 12; i++ {
		appendOne(t, o, db, "contact.created")
	}
	// Advance the clock well past the time horizon so the time floor no longer
	// protects these rows; now the row backstop (keep 5) governs.
	o.now = func() time.Time { return base.Add(30 * 24 * time.Hour) }

	if err := o.Trim(context.Background()); err != nil {
		t.Fatalf("trim: %v", err)
	}
	if got := countRows(t, o); got != 5 {
		t.Fatalf("after trim: got %d rows want 5 (newest)", got)
	}
	// The survivors must be the newest 5 (seq 8..12); MIN(seq) climbs.
	min, _ := o.minSeq(context.Background())
	if min != 8 {
		t.Fatalf("min seq after trim: got %d want 8", min)
	}
}

// TestTrim_TimeHorizonKeepsRecentEvenBeyondRowBackstop verifies the other side
// of the conservative floor: recent rows are NOT trimmed even when they exceed
// the row backstop, because the time horizon still protects them.
func TestTrim_TimeHorizonKeepsRecentEvenBeyondRowBackstop(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	o, db := newMemOutbox(t, func(opt *Options) {
		opt.RetentionMaxRows = 5
		opt.RetentionDays = 7
		opt.Now = func() time.Time { return now } // all rows are "now"
	})
	for i := 0; i < 12; i++ {
		appendOne(t, o, db, "contact.created")
	}
	if err := o.Trim(context.Background()); err != nil {
		t.Fatalf("trim: %v", err)
	}
	if got := countRows(t, o); got != 12 {
		t.Fatalf("recent rows must survive: got %d want 12", got)
	}
}

func TestReclaim_Runs(t *testing.T) {
	o, db := newMemOutbox(t)
	for i := 0; i < 3; i++ {
		appendOne(t, o, db, "contact.created")
	}
	if err := o.Reclaim(context.Background()); err != nil {
		t.Fatalf("reclaim: %v", err)
	}
}
