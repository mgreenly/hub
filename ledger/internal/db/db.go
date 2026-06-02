// Package db owns the SQLite handle and the homegrown migration runner.
package db

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// Open opens the SQLite database at path with the pragmas the spec requires.
// A single connection enforces single-writer discipline for the embedded
// SQLite store.
func Open(dbPath string) (*sql.DB, error) {
	dsn := fmt.Sprintf(
		"file:%s?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)",
		dbPath,
	)
	conn, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %s: %w", dbPath, err)
	}
	conn.SetMaxOpenConns(1)
	if err := conn.Ping(); err != nil {
		conn.Close()
		return nil, fmt.Errorf("ping sqlite %s: %w", dbPath, err)
	}
	return conn, nil
}

type migration struct {
	version int
	name    string
	sql     string
}

func loadMigrations() ([]migration, error) {
	return loadMigrationsFS(migrationsFS, "migrations")
}

func loadMigrationsFS(fsys fs.FS, dir string) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, dir)
	if err != nil {
		return nil, fmt.Errorf("read migrations dir: %w", err)
	}
	var out []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		under := strings.IndexByte(e.Name(), '_')
		if under <= 0 {
			return nil, fmt.Errorf("migration %q: expected NNN_name.sql", e.Name())
		}
		v, err := strconv.Atoi(e.Name()[:under])
		if err != nil {
			return nil, fmt.Errorf("migration %q: parse version: %w", e.Name(), err)
		}
		body, err := fs.ReadFile(fsys, path.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", e.Name(), err)
		}
		out = append(out, migration{version: v, name: e.Name(), sql: string(body)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i := 1; i < len(out); i++ {
		if out[i].version == out[i-1].version {
			return nil, fmt.Errorf("migration version %d duplicated (%s and %s)", out[i].version, out[i-1].name, out[i].name)
		}
	}
	return out, nil
}

// Migrate applies every embedded migration whose version is not yet recorded.
// Migration 001 establishes the schema_migrations table itself.
func Migrate(ctx context.Context, conn *sql.DB) error {
	migs, err := loadMigrations()
	if err != nil {
		return err
	}
	return runMigrations(ctx, conn, migs)
}

func runMigrations(ctx context.Context, conn *sql.DB, migs []migration) error {
	if len(migs) == 0 {
		return nil
	}
	applied := map[int]bool{}
	if exists, err := tableExists(ctx, conn, "schema_migrations"); err != nil {
		return err
	} else if exists {
		rows, err := conn.QueryContext(ctx, `SELECT version FROM schema_migrations`)
		if err != nil {
			return fmt.Errorf("select schema_migrations: %w", err)
		}
		for rows.Next() {
			var v int
			if err := rows.Scan(&v); err != nil {
				rows.Close()
				return fmt.Errorf("scan schema_migrations: %w", err)
			}
			applied[v] = true
		}
		rows.Close()
		// Refuse to downgrade: an applied version not present in the embedded set.
		embedded := map[int]bool{}
		for _, m := range migs {
			embedded[m.version] = true
		}
		for v := range applied {
			if !embedded[v] {
				return fmt.Errorf("database has migration version %d that is not embedded in this binary; refusing to downgrade", v)
			}
		}
	}

	for _, m := range migs {
		if applied[m.version] {
			continue
		}
		if err := applyOne(ctx, conn, m); err != nil {
			return err
		}
	}
	return nil
}

func applyOne(ctx context.Context, conn *sql.DB, m migration) error {
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx for migration %d: %w", m.version, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, m.sql); err != nil {
		return fmt.Errorf("apply migration %s: %w", m.name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_migrations (version, applied_at) VALUES (?, ?)`,
		m.version, time.Now().UTC().Format(time.RFC3339Nano),
	); err != nil {
		return fmt.Errorf("record migration %d: %w", m.version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %d: %w", m.version, err)
	}
	return nil
}

func tableExists(ctx context.Context, conn *sql.DB, name string) (bool, error) {
	row := conn.QueryRowContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, name,
	)
	var got string
	switch err := row.Scan(&got); err {
	case nil:
		return true, nil
	case sql.ErrNoRows:
		return false, nil
	default:
		return false, err
	}
}
