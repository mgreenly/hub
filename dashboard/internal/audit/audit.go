// Package audit is the durable security-audit log. Each row is append-only; the
// service never updates or deletes rows once written (except the admin prune
// command). It records the auth/token/grant events the OAuth authorization
// server produces — federation, DCR, token issuance/refresh/reuse/revoke — plus
// session lifecycle and rate-limit events.
package audit

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"dashboard/internal/ids"
)

// EventType is the closed enumeration of audit event kinds.
type EventType string

const (
	EventFederationSuccess EventType = "federation.success"
	EventFederationReject  EventType = "federation.reject"
	EventDCRSuccess        EventType = "dcr.success"
	EventDCRReject         EventType = "dcr.reject"
	EventAuthcodeIssued    EventType = "oauth.authcode.issued"
	EventTokenIssued       EventType = "oauth.token.issued"
	EventTokenRefreshed    EventType = "oauth.token.refreshed"
	EventTokenReject       EventType = "oauth.token.reject"
	EventReuseDetected     EventType = "oauth.reuse.detected"
	EventChainRevoked      EventType = "oauth.chain.revoked"

	// Session lifecycle.
	EventSessionEstablished     EventType = "session.established"
	EventSessionLoggedOut       EventType = "session.logged_out"
	EventSessionIdleExpired     EventType = "session.idle_expired"
	EventSessionAbsoluteExpired EventType = "session.absolute_expired"

	// Rate-limit hit.
	EventRateLimitHit EventType = "rate_limit.hit"

	// Token-introspection (auth_request) decisions.
	EventAuthnAllow EventType = "authn.allow"
	EventAuthnDeny  EventType = "authn.deny"

	// Admin commands.
	EventAdminAuditPrune EventType = "admin.audit.prune"
	EventAdminPurge      EventType = "admin.purge"
)

// Event is one audit-log row.
type Event struct {
	Type       EventType
	OwnerEmail string // empty when unknown
	ClientID   string // empty when not OAuth
	ChainID    string // empty when not chain-scoped
	IP         string
	UserAgent  string
	Details    map[string]any
}

// Log writes events to the audit_log table.
type Log struct {
	DB  *sql.DB
	Now func() time.Time
}

// New constructs a Log over the given database handle.
func New(db *sql.DB) *Log {
	return &Log{DB: db, Now: time.Now}
}

// Write inserts an event. Empty fields are stored as NULL.
func (l *Log) Write(ctx context.Context, e Event) error {
	return l.write(ctx, l.DB, e)
}

// WriteTx inserts an event using a caller-owned transaction, so the audit row
// commits or rolls back together with the state change it records.
func (l *Log) WriteTx(ctx context.Context, tx *sql.Tx, e Event) error {
	return l.write(ctx, tx, e)
}

type execer interface {
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
}

func (l *Log) write(ctx context.Context, ex execer, e Event) error {
	now := l.Now().UTC()
	var details sql.NullString
	if len(e.Details) > 0 {
		b, err := json.Marshal(e.Details)
		if err != nil {
			return fmt.Errorf("marshal audit details: %w", err)
		}
		details = sql.NullString{String: string(b), Valid: true}
	}
	_, err := ex.ExecContext(ctx, `
		INSERT INTO audit_log (
			id, event_type, occurred_at, owner_email, client_id, chain_id, ip, user_agent, details
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		ids.New(),
		string(e.Type),
		now.Format(time.RFC3339Nano),
		nullable(e.OwnerEmail),
		nullable(e.ClientID),
		nullable(e.ChainID),
		nullable(e.IP),
		nullable(e.UserAgent),
		details,
	)
	if err != nil {
		return fmt.Errorf("insert audit_log: %w", err)
	}
	return nil
}

func nullable(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// FromRequest pulls IP and User-Agent from an HTTP request for the
// corresponding audit fields.
func FromRequest(r *http.Request) (ip, userAgent string) {
	if r == nil {
		return "", ""
	}
	return r.RemoteAddr, r.Header.Get("User-Agent")
}

// CountOlderThan returns the number of audit rows whose occurred_at is strictly
// before cutoff. Used by the audit prune dry-run.
func (l *Log) CountOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	var n int64
	err := l.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM audit_log WHERE occurred_at < ?`,
		cutoff.UTC().Format(time.RFC3339Nano),
	).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count audit_log: %w", err)
	}
	return n, nil
}

// Prune deletes audit rows older than cutoff and returns the count removed.
// Only the admin command invokes it.
func (l *Log) Prune(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := l.DB.ExecContext(ctx,
		`DELETE FROM audit_log WHERE occurred_at < ?`,
		cutoff.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return 0, fmt.Errorf("prune audit_log: %w", err)
	}
	n, _ := res.RowsAffected()
	return n, nil
}
