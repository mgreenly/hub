package audit_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"dashboard/internal/audit"
	"dashboard/internal/db"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	database, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { _ = database.Close() })
	return database
}

// onlyRow returns the last-scanned audit_log row's event_type and the row count.
func onlyRow(t *testing.T, database *sql.DB) (string, int) {
	t.Helper()
	rows, err := database.Query(`SELECT event_type FROM audit_log`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	var et string
	n := 0
	for rows.Next() {
		n++
		if err := rows.Scan(&et); err != nil {
			t.Fatalf("scan: %v", err)
		}
	}
	return et, n
}

func TestWriteInsertsRowWithDetails(t *testing.T) {
	database := openDB(t)
	log := audit.New(database)
	ctx := context.Background()

	e := audit.Event{
		Type:       audit.EventTokenIssued,
		OwnerEmail: "owner@example.com",
		ClientID:   "client-123",
		ChainID:    "chain-abc",
		IP:         "203.0.113.7",
		UserAgent:  "test-agent/1.0",
		Details:    map[string]any{"scope": "read", "count": float64(3)},
	}
	if err := log.Write(ctx, e); err != nil {
		t.Fatalf("Write: %v", err)
	}

	et, n := onlyRow(t, database)
	if n != 1 {
		t.Fatalf("expected 1 row, got %d", n)
	}
	if et != string(audit.EventTokenIssued) {
		t.Fatalf("event_type = %q, want %q", et, audit.EventTokenIssued)
	}

	// Verify the populated row, including JSON details, by re-reading it.
	var (
		owner, client, chain, ip, ua, details sql.NullString
		occurred                              string
	)
	err := database.QueryRow(`
		SELECT owner_email, client_id, chain_id, ip, user_agent, details, occurred_at
		FROM audit_log`).Scan(&owner, &client, &chain, &ip, &ua, &details, &occurred)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if owner.String != "owner@example.com" || client.String != "client-123" || chain.String != "chain-abc" {
		t.Fatalf("identity columns wrong: %+v %+v %+v", owner, client, chain)
	}
	if ip.String != "203.0.113.7" || ua.String != "test-agent/1.0" {
		t.Fatalf("ip/ua wrong: %+v %+v", ip, ua)
	}
	if !details.Valid {
		t.Fatalf("details should be non-NULL")
	}
	want := `{"count":3,"scope":"read"}`
	if details.String != want {
		t.Fatalf("details JSON = %q, want %q", details.String, want)
	}
	if occurred == "" {
		t.Fatalf("occurred_at should be set")
	}
}

func TestWriteNullsForEmptyFields(t *testing.T) {
	database := openDB(t)
	log := audit.New(database)
	ctx := context.Background()

	if err := log.Write(ctx, audit.Event{Type: audit.EventTokenReject}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	var (
		owner, client, chain, ip, ua, details sql.NullString
		eventType, occurred                   string
	)
	err := database.QueryRow(`
		SELECT event_type, owner_email, client_id, chain_id, ip, user_agent, details, occurred_at
		FROM audit_log`).Scan(&eventType, &owner, &client, &chain, &ip, &ua, &details, &occurred)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if eventType != string(audit.EventTokenReject) {
		t.Fatalf("event_type = %q", eventType)
	}
	for name, c := range map[string]sql.NullString{
		"owner_email": owner, "client_id": client, "chain_id": chain,
		"ip": ip, "user_agent": ua, "details": details,
	} {
		if c.Valid {
			t.Fatalf("%s should be NULL, got %q", name, c.String)
		}
	}
	if occurred == "" {
		t.Fatalf("occurred_at should always be set")
	}
}

func TestEventTypesRoundTrip(t *testing.T) {
	database := openDB(t)
	log := audit.New(database)
	ctx := context.Background()

	types := []audit.EventType{
		audit.EventDCRReject,
		audit.EventDCRSuccess,
		audit.EventReuseDetected,
		audit.EventTokenRefreshed,
		audit.EventChainRevoked,
	}
	for _, et := range types {
		if err := log.Write(ctx, audit.Event{Type: et}); err != nil {
			t.Fatalf("Write(%s): %v", et, err)
		}
	}

	rows, err := database.Query(`SELECT event_type FROM audit_log ORDER BY event_type`)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	defer rows.Close()
	got := map[string]bool{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatalf("scan: %v", err)
		}
		got[s] = true
	}
	for _, et := range types {
		if !got[string(et)] {
			t.Fatalf("missing event_type %q", et)
		}
	}
}

func TestWriteTxCommitVisible(t *testing.T) {
	database := openDB(t)
	log := audit.New(database)
	ctx := context.Background()

	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := log.WriteTx(ctx, tx, audit.Event{
		Type:       audit.EventChainRevoked,
		OwnerEmail: "owner@example.com",
	}); err != nil {
		t.Fatalf("WriteTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	et, n := onlyRow(t, database)
	if n != 1 {
		t.Fatalf("expected 1 row after commit, got %d", n)
	}
	if et != string(audit.EventChainRevoked) {
		t.Fatalf("event_type = %q", et)
	}
}

func TestWriteTxRollbackAbsent(t *testing.T) {
	database := openDB(t)
	log := audit.New(database)
	ctx := context.Background()

	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := log.WriteTx(ctx, tx, audit.Event{Type: audit.EventTokenIssued}); err != nil {
		t.Fatalf("WriteTx: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	_, n := onlyRow(t, database)
	if n != 0 {
		t.Fatalf("expected 0 rows after rollback, got %d", n)
	}
}
