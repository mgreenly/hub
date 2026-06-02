// Package consumer is the consumer half of the suite's internal SSE event plane
// (see ../../docs/event-protocol.md — the normative wire contract; on any
// conflict that doc wins). It is the mirror of package outbox: where outbox is
// the producer that serves GET /feed, consumer is the engine that connects to a
// feed, streams events past a durable per-upstream cursor, and invokes a handler
// for each one.
//
// The engine is domain-agnostic and effect-agnostic. It owns the feed_offset
// table (§10.3, SchemaSQL), the hand-rolled SSE client, the reconnect/backoff
// loop, and the connect-time resync handling (all four reasons of §10.1) — none
// of which a consuming service should re-implement. The service supplies only a
// Config and a Handler: the engine calls the handler for EVERY event (type
// filtering is the service's job, §7.3) and then commits the cursor regardless
// of what the handler returned — this engine drives the best-effort external-hop
// model (§11.2), so a handler error is logged and ignored, never retried, and
// never blocks the cursor advance (decision 1, 8).
//
// What is a STRUCTURAL fault versus a TRANSPORT fault is the load-bearing split
// (decision 11): a feed_offset read/write failure (a missing table, a closed DB
// — a deploy or programming bug) escapes Run so the process crashes and systemd
// restart-loops visibly; a connect failure, a non-200, a dropped connection, or
// a stalled keepalive is a transport fault the engine retries internally,
// indefinitely, with backoff (§10.1) — it never escapes Run.
package consumer

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"time"
)

// First-subscription positions (§7.1). `tail` streams only new events from the
// current head; `earliest` streams the entire retained outbox.
const (
	fromTail     = "tail"
	fromEarliest = "earliest"
)

// Backoff bounds for the reconnect loop (§10.1 SHOULD; decision 9: exponential
// + jitter, cap 30s, retry indefinitely). They are package vars, not consts, so
// in-package tests can shrink them; production never touches them.
var (
	baseBackoff = 250 * time.Millisecond
	maxBackoff  = 30 * time.Second
)

// Event is one event delivered to the handler, decomposed from the wire envelope
// (§8.3). Payload is the opaque per-type body (§8.6) — the engine never inspects
// it; the handler unmarshals the types it cares about.
type Event struct {
	Type    string          // the SSE event: line, e.g. "contact.created" (§8.5)
	ID      string          // envelope "id" — a ULID, the stable dedup key (§8.3)
	Source  string          // envelope "source", e.g. "crm" (§8.3)
	Time    string          // envelope "time", RFC 3339 (§8.3)
	Payload json.RawMessage // opaque domain snapshot (§8.6)
}

// Handler is the per-event effect. The engine invokes it for EVERY event (the
// service filters by Type itself, §7.3) and commits the cursor regardless of the
// return value: a non-nil error is logged and ignored (decision 8), because this
// engine drives the best-effort external-hop model (§11.2) where loss is
// tolerated and the cursor must always advance.
type Handler func(ctx context.Context, ev Event) error

// Config is the engine's injected configuration, read once at the composition
// root (§3). FeedURL, Source, and ConsumerID are the only peer-specific values;
// everything else has a safe default.
type Config struct {
	// FeedURL is the upstream producer's feed address (§3), e.g.
	// "http://127.0.0.1:3001/feed". Loopback-direct: the event plane bypasses
	// nginx (§2). Required.
	FeedURL string
	// From is the first-subscription choice (§7.1): "tail" (default) or
	// "earliest". It is consulted only on a fresh subscription; thereafter the
	// engine always presents its committed cursor.
	From string
	// DB is the consumer's own database, where the engine keeps the feed_offset
	// row (§10.3). The engine is the sole writer of that table. Required.
	DB *sql.DB
	// Source keys the feed_offset row (§9.1): the upstream's envelope "source",
	// e.g. "crm". Required.
	Source string
	// ConsumerID is the stable X-Consumer-Id sent on every connect (§7.1). A
	// fixed per-service constant, e.g. "notify". Required.
	ConsumerID string
	// Logger is used for feed observability. Defaults to slog.Default().
	Logger *slog.Logger
	// HTTPClient connects to the feed. It MUST have no overall timeout — the feed
	// is a long-lived stream (§6). Defaults to a zero-timeout client.
	HTTPClient *http.Client
	// Now is the clock for feed_offset timestamps, injectable for tests. Defaults
	// to time.Now.
	Now func() time.Time
}

// Run drives the consumer loop until ctx is cancelled (returns nil) or a
// STRUCTURAL fault occurs (returns the error so the process crashes — decision
// 11). It is meant to run as a background goroutine alongside the service's HTTP
// server, sharing the server's context so a SIGTERM cancels both.
func Run(ctx context.Context, cfg Config, h Handler) error {
	if cfg.DB == nil {
		return errors.New("consumer: DB is required")
	}
	if cfg.Source == "" {
		return errors.New("consumer: Source is required")
	}
	if cfg.ConsumerID == "" {
		return errors.New("consumer: ConsumerID is required")
	}
	if h == nil {
		return errors.New("consumer: Handler is required")
	}
	if _, err := url.Parse(cfg.FeedURL); err != nil || cfg.FeedURL == "" {
		return fmt.Errorf("consumer: FeedURL %q is invalid: %w", cfg.FeedURL, err)
	}
	from := cfg.From
	if from == "" {
		from = fromTail
	}
	if from != fromTail && from != fromEarliest {
		return fmt.Errorf("consumer: From must be %q or %q, got %q", fromTail, fromEarliest, cfg.From)
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	client := cfg.HTTPClient
	if client == nil {
		// No timeout: the feed is a long-lived stream (§6). Liveness is bounded by
		// the producer's keepalive (§10.1), surfaced as a read error if the pipe
		// dies — the engine then reconnects.
		client = &http.Client{}
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}

	e := &engine{
		cfg:    cfg,
		from:   from,
		log:    logger,
		client: client,
		h:      h,
		st:     &store{db: cfg.DB, source: cfg.Source, now: now},
	}
	return e.run(ctx)
}

type engine struct {
	cfg    Config
	from   string
	log    *slog.Logger
	client *http.Client
	h      Handler
	st     *store
}

// Sentinel errors that the frame callback returns to stop scanning. None
// escapes the engine: the attempt outcome is carried in attemptResult.
var (
	errStopResync     = errors.New("consumer: resync received")
	errStopStructural = errors.New("consumer: structural fault")
	errStopShutdown   = errors.New("consumer: shutdown")
)

// structural maps a feed_offset error to a Run return value: a context
// cancellation is a clean shutdown (nil), anything else is a structural fault
// the process must crash on (decision 11).
func structural(ctx context.Context, err error) error {
	if err == nil || ctx.Err() != nil {
		return nil
	}
	return err
}

// attemptResult is the outcome of one connection attempt. Exactly one of the
// terminal fields is meaningful; an all-zero result (no resync, no structErr,
// connected or not) is a transport failure the loop retries.
type attemptResult struct {
	connected bool   // a 200 + text/event-stream was received (we registered at head)
	resync    string // a fully-received resync reason (§10.1), authoritative
	structErr error  // a feed_offset fault — crash (decision 11)
}

// run is the bootstrap-then-reconnect loop. It (re)establishes the
// first-subscription marker, then repeatedly connects-and-streams, mapping each
// attempt's outcome to: crash (structural), re-bootstrap (resync), reset backoff
// (connected then dropped), or backoff-and-retry (transport).
func (e *engine) run(ctx context.Context) error {
	backoff := baseBackoff
	needBootstrap := true
	connectedOnce := false
	bootstrapTail := false

	for {
		if ctx.Err() != nil {
			return nil
		}

		if needBootstrap {
			state, err := e.st.load(ctx)
			if err != nil {
				return structural(ctx, err) // crash unless shutting down
			}
			// A genuinely fresh subscription: never marked, never committed. Only
			// then does the configured `tail` choice apply (§7.1).
			fresh := !state.subscribed && !state.cursor.Valid
			bootstrapTail = fresh && e.from == fromTail
			connectedOnce = false
			// Record the first-subscription choice durably BEFORE the first
			// connect (§7.1, §10): once subscribed=1, a cursor-less reconnect
			// resolves to "from the beginning" (over-delivery the best-effort hop
			// tolerates), never a fresh `tail` that would silently drop the gap.
			if !state.subscribed {
				if err := e.st.markSubscribed(ctx); err != nil {
					return structural(ctx, err) // crash unless shutting down
				}
			}
			needBootstrap = false
			e.log.Info("consumer: subscribing", "source", e.cfg.Source, "from", e.from, "tail_bootstrap", bootstrapTail)
		}

		state, err := e.st.load(ctx)
		if err != nil {
			return structural(ctx, err) // crash unless shutting down
		}

		res := e.connectAndStream(ctx, state, bootstrapTail && !connectedOnce)
		if res.structErr != nil {
			return structural(ctx, res.structErr) // crash (decision 11) unless shutting down
		}
		if res.connected {
			connectedOnce = true
			backoff = baseBackoff // a healthy connection resets the curve
		}
		if res.resync != "" {
			e.logResync(res.resync)
			if err := e.st.clearForResync(ctx); err != nil {
				return structural(ctx, err) // crash unless shutting down
			}
			// Reconnect fresh, honoring the configured bootstrap choice (decision
			// 9). No backoff: a resync is a clean reposition, not a failure.
			needBootstrap = true
			backoff = baseBackoff
			continue
		}

		// Transport failure (or a clean ctx cancellation): retry indefinitely
		// with the committed cursor, backing off (§10.1).
		if ctx.Err() != nil {
			return nil
		}
		if !e.sleep(ctx, jitter(backoff)) {
			return nil
		}
		backoff = nextBackoff(backoff)
	}
}

// connectAndStream opens one feed connection from the given durable position and
// streams it to completion. useTail issues ?from=tail (the one-time fresh-tail
// bootstrap); otherwise a valid cursor is presented as Last-Event-ID, and a
// missing cursor means "from the beginning" (§7.1).
func (e *engine) connectAndStream(ctx context.Context, state offsetState, useTail bool) attemptResult {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.cfg.FeedURL, nil)
	if err != nil {
		e.log.Warn("consumer: build request failed", "err", err, "source", e.cfg.Source)
		return attemptResult{}
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("X-Consumer-Id", e.cfg.ConsumerID) // §7.1: required on every connect

	switch {
	case state.cursor.Valid:
		// Resume strictly after the committed cursor (§7.2, §10) — never the last
		// one received.
		req.Header.Set("Last-Event-ID", state.cursor.String)
	case useTail:
		q := req.URL.Query()
		q.Set("from", fromTail)
		req.URL.RawQuery = q.Encode()
	default:
		// From the beginning: no Last-Event-ID, no ?from=tail (§7.1).
	}

	resp, err := e.client.Do(req)
	if err != nil {
		// Dial failure, upstream down at boot, ctx cancelled — all transport
		// (§10.1). Retry with the committed cursor.
		e.log.Warn("consumer: connect failed", "err", err, "source", e.cfg.Source, "url", e.cfg.FeedURL)
		return attemptResult{}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		e.log.Warn("consumer: feed returned non-200", "status", resp.StatusCode, "source", e.cfg.Source)
		return attemptResult{} // transport — retry
	}
	e.log.Info("consumer: connected", "source", e.cfg.Source, "url", e.cfg.FeedURL, "resuming", state.cursor.Valid, "tail", useTail)

	res := attemptResult{connected: true}
	_ = scanFrames(resp.Body, func(f sseFrame) error {
		return e.handleFrame(ctx, f, &res)
	})
	// scanFrames' returned error is the stop sentinel (resync/structural — already
	// captured in res) or a read error / clean EOF (transport — the zero result).
	// A fully-received resync is therefore authoritative regardless of how the
	// socket then closed (§10.1): it was captured before EOF could be seen.
	return res
}

// handleFrame dispatches one fully-received SSE frame. Only an event frame (with
// its id:) advances the cursor (§8.2); control and liveness frames never do.
func (e *engine) handleFrame(ctx context.Context, f sseFrame, res *attemptResult) error {
	switch f.event {
	case "resync":
		res.resync = parseReason(f.data)
		return errStopResync
	case "caught-up":
		// The producer states it has sent everything through its head (§10.1). We
		// are live; no cursor action — the marker was already made durable at
		// bootstrap.
		e.log.Debug("consumer: caught up", "source", e.cfg.Source)
		return nil
	case "status":
		// Pure telemetry (§10.1): log it, never act on it.
		e.log.Info("consumer: lag", "source", e.cfg.Source, "behind", parseBehind(f.data))
		return nil
	default:
		// Any other event: line is a domain event frame (§8.5 dotted types never
		// collide with the reserved control names above). An event frame MUST
		// carry an id (§8.1); without one it is malformed — ignore it without
		// advancing.
		if f.id == "" {
			e.log.Warn("consumer: event frame without id, ignoring", "event", f.event, "source", e.cfg.Source)
			return nil
		}
		ev, perr := parseEvent(f)
		if perr != nil {
			// We cannot interpret the envelope, but the cursor MUST still advance
			// past it (§7.3) or it re-arrives on every reconnect forever. Run no
			// effect; commit the cursor.
			e.log.Warn("consumer: unparseable envelope, skipping effect", "err", perr, "id", f.id, "source", e.cfg.Source)
		} else if herr := e.h(ctx, ev); herr != nil {
			// Best-effort: log and ignore, never retry, never block the commit
			// (decision 1, 8; §11.2).
			e.log.Warn("consumer: handler error (ignored)", "err", herr, "type", ev.Type, "event_id", ev.ID, "source", e.cfg.Source)
		}
		if err := e.st.commit(ctx, f.id); err != nil {
			if ctx.Err() != nil {
				// Cursor write interrupted by shutdown — not a structural fault.
				// At-least-once is preserved: this event re-delivers on the next
				// connect (§10), and a duplicate push is tolerated (§11.2).
				return errStopShutdown
			}
			// feed_offset write failed for a real reason — structural (decision
			// 11). Crash.
			res.structErr = err
			return errStopStructural
		}
		return nil
	}
}

// logResync logs a resync at the severity its meaning warrants (decision 9):
// past-horizon is real, unrecovered data loss (§11.1) and is logged loud at
// ERROR with a machine-greppable marker; the other three are position-validity
// faults.
func (e *engine) logResync(reason string) {
	switch reason {
	case "stale-epoch":
		e.log.Info("consumer: resync — stale epoch (normal post-restore/rebuild)", "reason", reason, "source", e.cfg.Source)
	case "diverged":
		e.log.Warn("consumer: resync — diverged", "reason", reason, "source", e.cfg.Source)
	case "past-horizon":
		e.log.Error("consumer: resync — DATA LOSS past horizon", "event", "past_horizon_data_loss", "reason", reason, "source", e.cfg.Source)
	case "unintelligible-cursor":
		e.log.Error("consumer: resync — unintelligible cursor (should not happen)", "reason", reason, "source", e.cfg.Source)
	default:
		e.log.Warn("consumer: resync — unknown reason", "reason", reason, "source", e.cfg.Source)
	}
}

// sleep waits d or until ctx is cancelled; it reports false if cancelled.
func (e *engine) sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// nextBackoff doubles the delay, capped at maxBackoff.
func nextBackoff(d time.Duration) time.Duration {
	d *= 2
	if d > maxBackoff {
		return maxBackoff
	}
	return d
}

// jitter returns a randomized delay in [d/2, d] (full-ish jitter) so a fleet of
// reconnecting consumers does not synchronize (§10.1 SHOULD).
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	half := d / 2
	return half + time.Duration(rand.Int64N(int64(half)+1))
}

// parseEvent decomposes an event frame's envelope (§8.3) into an Event. Type is
// taken from the SSE event: line (§8.1) — it mirrors the envelope type.
func parseEvent(f sseFrame) (Event, error) {
	var env struct {
		ID      string          `json:"id"`
		Type    string          `json:"type"`
		Source  string          `json:"source"`
		Time    string          `json:"time"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal([]byte(f.data), &env); err != nil {
		return Event{}, fmt.Errorf("unmarshal envelope: %w", err)
	}
	return Event{
		Type:    f.event,
		ID:      env.ID,
		Source:  env.Source,
		Time:    env.Time,
		Payload: env.Payload,
	}, nil
}

// parseReason extracts the resync reason from a {"reason":"…"} data body
// (§10.1). An unparseable body yields "" — logged as an unknown reason.
func parseReason(data string) string {
	var d struct {
		Reason string `json:"reason"`
	}
	_ = json.Unmarshal([]byte(data), &d)
	return d.Reason
}

// parseBehind extracts the lag integer from a {"behind":<int>} status body
// (§10.1).
func parseBehind(data string) int64 {
	var d struct {
		Behind int64 `json:"behind"`
	}
	_ = json.Unmarshal([]byte(data), &d)
	return d.Behind
}
