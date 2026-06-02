package db

import (
	"database/sql"
	"path/filepath"
	"testing"
)

func countRows(t *testing.T, database *sql.DB, query string) int {
	t.Helper()
	var n int
	if err := database.QueryRow(query).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// TestOpenCreatesSchema confirms Open migrates a fresh database: the app table and
// the bookkeeping table exist, and at least one migration is recorded.
func TestOpenCreatesSchema(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()

	for _, table := range []string{"oauth_state", "schema_migrations"} {
		var n int
		if err := database.QueryRow(
			`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?`, table,
		).Scan(&n); err != nil {
			t.Fatalf("query sqlite_master: %v", err)
		}
		if n != 1 {
			t.Errorf("table %q missing after Open", table)
		}
	}
	if countRows(t, database, `SELECT COUNT(*) FROM schema_migrations`) == 0 {
		t.Error("no migrations recorded")
	}
}

// TestOpenIsIdempotent reopens the same file and confirms migrations are not
// re-applied — the count of recorded migrations is unchanged.
func TestOpenIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.db")

	first, err := Open(path)
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	afterFirst := countRows(t, first, `SELECT COUNT(*) FROM schema_migrations`)
	first.Close()

	second, err := Open(path)
	if err != nil {
		t.Fatalf("second Open: %v", err)
	}
	defer second.Close()
	afterSecond := countRows(t, second, `SELECT COUNT(*) FROM schema_migrations`)

	if afterSecond != afterFirst {
		t.Errorf("migration count changed on re-open: %d -> %d (re-applied)", afterFirst, afterSecond)
	}
}

// TestForeignKeysEnabled confirms the DSN pragma reached the connection.
func TestForeignKeysEnabled(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "x.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer database.Close()
	if on := countRows(t, database, `PRAGMA foreign_keys`); on != 1 {
		t.Errorf("foreign_keys = %d, want 1", on)
	}
}
