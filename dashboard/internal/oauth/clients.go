package oauth

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"dashboard/internal/ids"
)

// ClientStore persists DCR client registrations.
type ClientStore struct {
	DB  *sql.DB
	Now func() time.Time
}

func NewClientStore(db *sql.DB) *ClientStore {
	return &ClientStore{DB: db, Now: time.Now}
}

// Register inserts a new client_id with the given redirect URIs. Retries on
// the unlikely event of a client_id collision (never overwrite).
func (s *ClientStore) Register(ctx context.Context, clientName string, redirectURIs []string) (Client, error) {
	if len(redirectURIs) == 0 {
		return Client{}, errors.New("redirect_uris empty")
	}
	urisJSON, err := json.Marshal(redirectURIs)
	if err != nil {
		return Client{}, fmt.Errorf("marshal redirect_uris: %w", err)
	}
	now := s.Now().UTC()
	for attempt := 0; attempt < 4; attempt++ {
		clientID := ids.New()
		_, err := s.DB.ExecContext(ctx, `
			INSERT INTO dcr_clients (id, client_id, client_name, redirect_uris, registered_at)
			VALUES (?, ?, ?, ?, ?)
		`, ids.New(), clientID, nullableStr(clientName), string(urisJSON), now.Format(time.RFC3339Nano))
		if err == nil {
			return Client{
				ClientID:     clientID,
				ClientName:   clientName,
				RedirectURIs: redirectURIs,
				RegisteredAt: now,
			}, nil
		}
	}
	return Client{}, errors.New("failed to allocate unique client_id after retries")
}

// Get fetches a client by client_id.
func (s *ClientStore) Get(ctx context.Context, clientID string) (Client, error) {
	row := s.DB.QueryRowContext(ctx, `
		SELECT client_id, client_name, redirect_uris, registered_at, last_used_at
		FROM dcr_clients WHERE client_id = ?
	`, clientID)
	var (
		c          Client
		name       sql.NullString
		urisJSON   string
		registered string
		lastUsed   sql.NullString
	)
	err := row.Scan(&c.ClientID, &name, &urisJSON, &registered, &lastUsed)
	if errors.Is(err, sql.ErrNoRows) {
		return Client{}, ErrNotFound
	}
	if err != nil {
		return Client{}, fmt.Errorf("select dcr_clients: %w", err)
	}
	if name.Valid {
		c.ClientName = name.String
	}
	if err := json.Unmarshal([]byte(urisJSON), &c.RedirectURIs); err != nil {
		return Client{}, fmt.Errorf("unmarshal redirect_uris: %w", err)
	}
	c.RegisteredAt, _ = time.Parse(time.RFC3339Nano, registered)
	if lastUsed.Valid {
		t, _ := time.Parse(time.RFC3339Nano, lastUsed.String)
		c.LastUsedAt = &t
	}
	return c, nil
}

// TouchLastUsed updates last_used_at on a successful token issuance.
func (s *ClientStore) TouchLastUsed(ctx context.Context, clientID string) error {
	now := s.Now().UTC().Format(time.RFC3339Nano)
	_, err := s.DB.ExecContext(ctx, `UPDATE dcr_clients SET last_used_at = ? WHERE client_id = ?`, now, clientID)
	if err != nil {
		return fmt.Errorf("touch dcr_clients: %w", err)
	}
	return nil
}

// PurgeUnused removes DCR rows that never produced a token chain and are
// older than the cutoff. Returns the row count deleted.
func (s *ClientStore) PurgeUnused(ctx context.Context, cutoff time.Time) (int64, error) {
	res, err := s.DB.ExecContext(ctx, `
		DELETE FROM dcr_clients WHERE last_used_at IS NULL AND registered_at < ?
	`, cutoff.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0, fmt.Errorf("purge dcr_clients: %w", err)
	}
	return res.RowsAffected()
}

func nullableStr(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}
