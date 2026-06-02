package contacts

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Repo is the SQL-only data layer. Every method takes *sql.Tx so the service
// composes transactions.
type Repo struct{}

func NewRepo() *Repo { return &Repo{} }

const rfc = time.RFC3339Nano

// ── contacts ──────────────────────────────────────────────────────────────

func (Repo) InsertContact(tx *sql.Tx, c Contact) error {
	_, err := tx.Exec(`
		INSERT INTO contacts (id, given_name, family_name, display_name, created_at, updated_at, deleted_at)
		VALUES (?, ?, ?, ?, ?, ?, NULL)
	`, c.ID, nullString(c.GivenName), nullString(c.FamilyName), c.DisplayName,
		c.CreatedAt.UTC().Format(rfc), c.UpdatedAt.UTC().Format(rfc))
	if err != nil {
		return fmt.Errorf("insert contact: %w", err)
	}
	return nil
}

func (Repo) GetContact(tx *sql.Tx, id string, includeDeleted bool) (Contact, error) {
	q := `SELECT id, given_name, family_name, display_name, created_at, updated_at, deleted_at FROM contacts WHERE id = ?`
	if !includeDeleted {
		q += ` AND deleted_at IS NULL`
	}
	row := tx.QueryRow(q, id)
	return scanContact(row)
}

// UpdateContactNames updates whichever of given/family/display are non-nil.
// Returns ErrNotFound if no live row matches.
func (Repo) UpdateContactNames(tx *sql.Tx, id string, given, family, display *string, updatedAt time.Time) error {
	sets := []string{"updated_at = ?"}
	args := []any{updatedAt.UTC().Format(rfc)}
	if given != nil {
		sets = append(sets, "given_name = ?")
		args = append(args, nullString(given))
	}
	if family != nil {
		sets = append(sets, "family_name = ?")
		args = append(args, nullString(family))
	}
	if display != nil {
		sets = append(sets, "display_name = ?")
		args = append(args, *display)
	}
	args = append(args, id)
	res, err := tx.Exec(
		`UPDATE contacts SET `+strings.Join(sets, ", ")+` WHERE id = ? AND deleted_at IS NULL`,
		args...,
	)
	if err != nil {
		return fmt.Errorf("update contact: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SoftDeleteContact stamps deleted_at on the contact row only. Children are
// cascaded by the service.
func (Repo) SoftDeleteContact(tx *sql.Tx, id string, at time.Time) error {
	res, err := tx.Exec(`UPDATE contacts SET deleted_at = ?, updated_at = ? WHERE id = ? AND deleted_at IS NULL`,
		at.UTC().Format(rfc), at.UTC().Format(rfc), id)
	if err != nil {
		return fmt.Errorf("soft delete contact: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SoftDeleteChildrenOfContact cascades the soft-delete to child emails/phones
// in the same tx. Idempotent on already-soft-deleted children.
func (Repo) SoftDeleteChildrenOfContact(tx *sql.Tx, contactID string, at time.Time) error {
	ts := at.UTC().Format(rfc)
	if _, err := tx.Exec(`UPDATE contact_emails SET deleted_at = ? WHERE contact_id = ? AND deleted_at IS NULL`, ts, contactID); err != nil {
		return fmt.Errorf("soft delete contact_emails: %w", err)
	}
	if _, err := tx.Exec(`UPDATE contact_phones SET deleted_at = ? WHERE contact_id = ? AND deleted_at IS NULL`, ts, contactID); err != nil {
		return fmt.Errorf("soft delete contact_phones: %w", err)
	}
	return nil
}

// ListContacts returns a page of contacts ordered by id ASC. q does
// case-insensitive substring matching across name + child email + child phone.
func (Repo) ListContacts(tx *sql.Tx, p ListParams) ([]Contact, error) {
	var (
		where []string
		args  []any
	)
	if !p.IncludeDeleted {
		where = append(where, `c.deleted_at IS NULL`)
	}
	if p.AfterID != "" {
		where = append(where, `c.id > ?`)
		args = append(args, p.AfterID)
	}
	if p.Q != "" {
		like := "%" + p.Q + "%"
		where = append(where, `(
			c.display_name LIKE ? COLLATE NOCASE
			OR c.given_name  LIKE ? COLLATE NOCASE
			OR c.family_name LIKE ? COLLATE NOCASE
			OR EXISTS (SELECT 1 FROM contact_emails e WHERE e.contact_id = c.id AND e.deleted_at IS NULL AND e.email LIKE ? COLLATE NOCASE)
			OR EXISTS (SELECT 1 FROM contact_phones ph WHERE ph.contact_id = c.id AND ph.deleted_at IS NULL AND ph.phone LIKE ? COLLATE NOCASE)
		)`)
		args = append(args, like, like, like, like, like)
	}
	limit := p.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	q := `SELECT c.id, c.given_name, c.family_name, c.display_name, c.created_at, c.updated_at, c.deleted_at FROM contacts c`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY c.id ASC LIMIT ?"
	args = append(args, limit)
	rows, err := tx.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("list contacts: %w", err)
	}
	defer rows.Close()
	var out []Contact
	for rows.Next() {
		c, err := scanContact(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ── emails ────────────────────────────────────────────────────────────────

func (Repo) InsertEmail(tx *sql.Tx, e Email) error {
	_, err := tx.Exec(`
		INSERT INTO contact_emails (id, contact_id, email, label, is_primary, created_at, deleted_at)
		VALUES (?, ?, ?, ?, ?, ?, NULL)
	`, e.ID, e.ContactID, e.Email, nullString(e.Label), boolInt(e.IsPrimary), e.CreatedAt.UTC().Format(rfc))
	if err != nil {
		return mapUniqueErr(err, "email")
	}
	return nil
}

func (Repo) GetEmail(tx *sql.Tx, contactID, emailID string) (Email, error) {
	row := tx.QueryRow(`
		SELECT id, contact_id, email, label, is_primary, created_at, deleted_at
		FROM contact_emails WHERE id = ? AND contact_id = ? AND deleted_at IS NULL
	`, emailID, contactID)
	return scanEmail(row)
}

func (Repo) ListEmails(tx *sql.Tx, contactID string) ([]Email, error) {
	rows, err := tx.Query(`
		SELECT id, contact_id, email, label, is_primary, created_at, deleted_at
		FROM contact_emails WHERE contact_id = ? AND deleted_at IS NULL
		ORDER BY created_at ASC, id ASC
	`, contactID)
	if err != nil {
		return nil, fmt.Errorf("list emails: %w", err)
	}
	defer rows.Close()
	var out []Email
	for rows.Next() {
		e, err := scanEmail(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountLiveEmails returns the number of live emails for a contact.
func (Repo) CountLiveEmails(tx *sql.Tx, contactID string) (int, error) {
	var n int
	err := tx.QueryRow(`SELECT COUNT(*) FROM contact_emails WHERE contact_id = ? AND deleted_at IS NULL`, contactID).Scan(&n)
	return n, err
}

// FindPrimaryEmail returns the current primary email row id, or sql.ErrNoRows
// if none.
func (Repo) FindPrimaryEmail(tx *sql.Tx, contactID string) (string, error) {
	var id string
	err := tx.QueryRow(`
		SELECT id FROM contact_emails
		WHERE contact_id = ? AND is_primary = 1 AND deleted_at IS NULL
	`, contactID).Scan(&id)
	return id, err
}

func (Repo) DemoteEmail(tx *sql.Tx, emailID string) error {
	_, err := tx.Exec(`UPDATE contact_emails SET is_primary = 0 WHERE id = ?`, emailID)
	return err
}

func (Repo) SetEmailLabel(tx *sql.Tx, emailID string, label *string) error {
	_, err := tx.Exec(`UPDATE contact_emails SET label = ? WHERE id = ? AND deleted_at IS NULL`, nullString(label), emailID)
	return err
}

func (Repo) PromoteEmail(tx *sql.Tx, emailID string) error {
	_, err := tx.Exec(`UPDATE contact_emails SET is_primary = 1 WHERE id = ? AND deleted_at IS NULL`, emailID)
	return err
}

func (Repo) SoftDeleteEmail(tx *sql.Tx, contactID, emailID string, at time.Time) error {
	res, err := tx.Exec(`UPDATE contact_emails SET deleted_at = ? WHERE id = ? AND contact_id = ? AND deleted_at IS NULL`,
		at.UTC().Format(rfc), emailID, contactID)
	if err != nil {
		return fmt.Errorf("soft delete email: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ── phones (mirror of emails) ─────────────────────────────────────────────

func (Repo) InsertPhone(tx *sql.Tx, p Phone) error {
	_, err := tx.Exec(`
		INSERT INTO contact_phones (id, contact_id, phone, label, is_primary, created_at, deleted_at)
		VALUES (?, ?, ?, ?, ?, ?, NULL)
	`, p.ID, p.ContactID, p.Phone, nullString(p.Label), boolInt(p.IsPrimary), p.CreatedAt.UTC().Format(rfc))
	if err != nil {
		return mapUniqueErr(err, "phone")
	}
	return nil
}

func (Repo) GetPhone(tx *sql.Tx, contactID, phoneID string) (Phone, error) {
	row := tx.QueryRow(`
		SELECT id, contact_id, phone, label, is_primary, created_at, deleted_at
		FROM contact_phones WHERE id = ? AND contact_id = ? AND deleted_at IS NULL
	`, phoneID, contactID)
	return scanPhone(row)
}

func (Repo) ListPhones(tx *sql.Tx, contactID string) ([]Phone, error) {
	rows, err := tx.Query(`
		SELECT id, contact_id, phone, label, is_primary, created_at, deleted_at
		FROM contact_phones WHERE contact_id = ? AND deleted_at IS NULL
		ORDER BY created_at ASC, id ASC
	`, contactID)
	if err != nil {
		return nil, fmt.Errorf("list phones: %w", err)
	}
	defer rows.Close()
	var out []Phone
	for rows.Next() {
		p, err := scanPhone(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (Repo) CountLivePhones(tx *sql.Tx, contactID string) (int, error) {
	var n int
	err := tx.QueryRow(`SELECT COUNT(*) FROM contact_phones WHERE contact_id = ? AND deleted_at IS NULL`, contactID).Scan(&n)
	return n, err
}

func (Repo) FindPrimaryPhone(tx *sql.Tx, contactID string) (string, error) {
	var id string
	err := tx.QueryRow(`
		SELECT id FROM contact_phones
		WHERE contact_id = ? AND is_primary = 1 AND deleted_at IS NULL
	`, contactID).Scan(&id)
	return id, err
}

func (Repo) DemotePhone(tx *sql.Tx, phoneID string) error {
	_, err := tx.Exec(`UPDATE contact_phones SET is_primary = 0 WHERE id = ?`, phoneID)
	return err
}

func (Repo) SetPhoneLabel(tx *sql.Tx, phoneID string, label *string) error {
	_, err := tx.Exec(`UPDATE contact_phones SET label = ? WHERE id = ? AND deleted_at IS NULL`, nullString(label), phoneID)
	return err
}

func (Repo) PromotePhone(tx *sql.Tx, phoneID string) error {
	_, err := tx.Exec(`UPDATE contact_phones SET is_primary = 1 WHERE id = ? AND deleted_at IS NULL`, phoneID)
	return err
}

func (Repo) SoftDeletePhone(tx *sql.Tx, contactID, phoneID string, at time.Time) error {
	res, err := tx.Exec(`UPDATE contact_phones SET deleted_at = ? WHERE id = ? AND contact_id = ? AND deleted_at IS NULL`,
		at.UTC().Format(rfc), phoneID, contactID)
	if err != nil {
		return fmt.Errorf("soft delete phone: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// ── scan helpers ──────────────────────────────────────────────────────────

type rowScanner interface {
	Scan(dest ...any) error
}

func scanContact(r rowScanner) (Contact, error) {
	var (
		c                    Contact
		given, family        sql.NullString
		createdAt, updatedAt string
		deletedAt            sql.NullString
	)
	if err := r.Scan(&c.ID, &given, &family, &c.DisplayName, &createdAt, &updatedAt, &deletedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Contact{}, ErrNotFound
		}
		return Contact{}, fmt.Errorf("scan contact: %w", err)
	}
	if given.Valid {
		c.GivenName = &given.String
	}
	if family.Valid {
		c.FamilyName = &family.String
	}
	c.CreatedAt, _ = time.Parse(rfc, createdAt)
	c.UpdatedAt, _ = time.Parse(rfc, updatedAt)
	if deletedAt.Valid {
		t, _ := time.Parse(rfc, deletedAt.String)
		c.DeletedAt = &t
	}
	return c, nil
}

func scanEmail(r rowScanner) (Email, error) {
	var (
		e         Email
		label     sql.NullString
		createdAt string
		deletedAt sql.NullString
		isPrimary int
	)
	if err := r.Scan(&e.ID, &e.ContactID, &e.Email, &label, &isPrimary, &createdAt, &deletedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Email{}, ErrNotFound
		}
		return Email{}, fmt.Errorf("scan email: %w", err)
	}
	if label.Valid {
		e.Label = &label.String
	}
	e.IsPrimary = isPrimary != 0
	e.CreatedAt, _ = time.Parse(rfc, createdAt)
	if deletedAt.Valid {
		t, _ := time.Parse(rfc, deletedAt.String)
		e.DeletedAt = &t
	}
	return e, nil
}

func scanPhone(r rowScanner) (Phone, error) {
	var (
		p         Phone
		label     sql.NullString
		createdAt string
		deletedAt sql.NullString
		isPrimary int
	)
	if err := r.Scan(&p.ID, &p.ContactID, &p.Phone, &label, &isPrimary, &createdAt, &deletedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Phone{}, ErrNotFound
		}
		return Phone{}, fmt.Errorf("scan phone: %w", err)
	}
	if label.Valid {
		p.Label = &label.String
	}
	p.IsPrimary = isPrimary != 0
	p.CreatedAt, _ = time.Parse(rfc, createdAt)
	if deletedAt.Valid {
		t, _ := time.Parse(rfc, deletedAt.String)
		p.DeletedAt = &t
	}
	return p, nil
}

func nullString(s *string) any {
	if s == nil {
		return nil
	}
	return *s
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// mapUniqueErr translates a SQLite UNIQUE-violation into ErrConflict.
func mapUniqueErr(err error, kind string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	if strings.Contains(msg, "UNIQUE constraint failed") || strings.Contains(msg, "constraint failed") {
		return fmt.Errorf("%w: duplicate %s", ErrConflict, kind)
	}
	return fmt.Errorf("insert %s: %w", kind, err)
}
