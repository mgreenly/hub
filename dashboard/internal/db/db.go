// Package db opens the dashboard's SQLite database and applies its migrations.
package db

import (
	"database/sql"
	"embed"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// Open opens the SQLite database at path with the pragmas the dashboard relies
// on (WAL journaling, enforced foreign keys, a 5s busy timeout), verifies the
// connection, then applies any pending migrations. The pragmas are set in the
// DSN so every pooled connection inherits them.
func Open(path string) (*sql.DB, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=foreign_keys(on)&_pragma=journal_mode(wal)"
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", path, err)
	}
	// One box, one writer: cap the pool at a single connection so concurrent
	// writes serialize in Go rather than racing for the SQLite file lock.
	database.SetMaxOpenConns(1)
	if err := database.Ping(); err != nil {
		database.Close()
		return nil, fmt.Errorf("ping sqlite %q: %w", path, err)
	}
	if err := migrate(database); err != nil {
		database.Close()
		return nil, err
	}
	return database, nil
}

// migrate applies every pending migration to database in filename order. Applied
// migrations are recorded by name in schema_migrations, so each runs exactly
// once across restarts. Embedded entries are already sorted by filename, and the
// datetime prefix (YYYYMMDDHHMMSS_, e.g. 20260530122721_) fixes the order.
func migrate(database *sql.DB) error {
	if _, err := database.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			name       TEXT PRIMARY KEY,
			applied_at TEXT NOT NULL
		)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	appliedMigrations, err := loadAppliedMigrations(database)
	if err != nil {
		return err
	}

	entries, err := migrationFiles.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("list migrations: %w", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if appliedMigrations[name] {
			continue
		}
		statements, err := migrationFiles.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if err := applyMigration(database, name, string(statements)); err != nil {
			return err
		}
	}
	return nil
}

// loadAppliedMigrations returns the set of migration names already recorded in
// schema_migrations.
func loadAppliedMigrations(database *sql.DB) (map[string]bool, error) {
	rows, err := database.Query(`SELECT name FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read schema_migrations: %w", err)
	}
	defer rows.Close()

	appliedMigrations := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan schema_migrations: %w", err)
		}
		appliedMigrations[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate schema_migrations: %w", err)
	}
	return appliedMigrations, nil
}

// applyMigration runs one migration's statements and records it in
// schema_migrations, both inside a single transaction so a failure leaves no
// half-applied schema and no orphan bookkeeping row.
func applyMigration(database *sql.DB, name, statements string) error {
	tx, err := database.Begin()
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", name, err)
	}
	if _, err := tx.Exec(statements); err != nil {
		tx.Rollback()
		return fmt.Errorf("apply migration %s: %w", name, err)
	}
	if _, err := tx.Exec(
		`INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`,
		name, time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		tx.Rollback()
		return fmt.Errorf("record migration %s: %w", name, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", name, err)
	}
	return nil
}
