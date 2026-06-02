package outbox

import (
	"context"
	"fmt"
	"time"
)

// retention timer cadences. Trimming is frequent and cheap; space reclamation
// (VACUUM) is heavier so it runs daily. Both are off the hot path (§11.3, §5.3).
const (
	trimInterval    = time.Hour
	reclaimInterval = 24 * time.Hour
)

// StartRetention runs the background retention job until ctx is cancelled
// (§11.3). It trims the outbox on trimInterval and reclaims freed space on
// reclaimInterval. It is decoupled from Append so the write path never pays for
// it. Run it in its own goroutine: `go ob.StartRetention(ctx)`.
func (o *Outbox) StartRetention(ctx context.Context) {
	if err := o.Trim(ctx); err != nil {
		o.log.Error("retention: initial trim failed", "err", err)
	}
	trim := time.NewTicker(trimInterval)
	defer trim.Stop()
	reclaim := time.NewTicker(reclaimInterval)
	defer reclaim.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-trim.C:
			if err := o.Trim(ctx); err != nil {
				o.log.Error("retention: trim failed", "err", err)
			}
		case <-reclaim.C:
			if err := o.Reclaim(ctx); err != nil {
				o.log.Error("retention: reclaim failed", "err", err)
			}
		}
	}
}

// Trim deletes events that have fallen beyond the retention horizon (§11.3).
// Two floors are computed — a time horizon (RetentionDays) and a row backstop
// (RetentionMaxRows) — and the MORE CONSERVATIVE floor wins: a row is trimmed
// only when it is beyond BOTH horizons. This keeps the outbox generous (the
// horizon is a contract, not a hope) while bounding unbounded growth.
func (o *Outbox) Trim(ctx context.Context) error {
	var maxSeq int64
	if err := o.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM outbox`).Scan(&maxSeq); err != nil {
		return fmt.Errorf("retention: max seq: %w", err)
	}
	if maxSeq == 0 {
		return nil // empty
	}

	// Row backstop: keep the newest retentionRows rows -> delete seq <= maxSeq-rows.
	rowFloor := maxSeq - o.retentionRows

	// Time horizon: delete rows older than the cutoff -> the highest such seq.
	cutoff := o.now().UTC().Add(-time.Duration(o.retentionDays) * 24 * time.Hour).Format(time.RFC3339Nano)
	var timeFloor int64
	if err := o.db.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) FROM outbox WHERE created_at < ?`, cutoff,
	).Scan(&timeFloor); err != nil {
		return fmt.Errorf("retention: time floor: %w", err)
	}

	// More conservative = trim less = the smaller delete-floor.
	floor := rowFloor
	if timeFloor < floor {
		floor = timeFloor
	}
	if floor <= 0 {
		return nil // nothing both old enough and beyond the row backstop
	}

	res, err := o.db.ExecContext(ctx, `DELETE FROM outbox WHERE seq <= ?`, floor)
	if err != nil {
		return fmt.Errorf("retention: delete: %w", err)
	}
	if n, _ := res.RowsAffected(); n > 0 {
		o.log.Info("retention: trimmed", "rows", n, "floor_seq", floor)
	}
	return nil
}

// Reclaim returns space freed by Trim to the filesystem (§11.3). Plain DELETE
// does not shrink a SQLite file, so under continuous insert-and-trim the file
// and WAL would grow without bound. wal_checkpoint(TRUNCATE) caps the WAL and
// VACUUM rebuilds the main file. auto_vacuum is deliberately NOT enabled — it
// would force a full vacuum of the already-populated consumer DB.
func (o *Outbox) Reclaim(ctx context.Context) error {
	if _, err := o.db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return fmt.Errorf("retention: wal_checkpoint: %w", err)
	}
	if _, err := o.db.ExecContext(ctx, `VACUUM`); err != nil {
		return fmt.Errorf("retention: vacuum: %w", err)
	}
	return nil
}
