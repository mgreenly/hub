package contacts

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"crm/internal/ids"

	"eventplane/outbox"
)

// Service owns transactions and enforces business rules (R-CTNS-PRIM primary
// semantics, R-CTNS-SDEL soft-delete cascade).
type Service struct {
	DB   *sql.DB
	Repo *Repo
	Now  func() time.Time
	// Outbox, when set, makes the service an event-plane producer: a
	// contact.created event is appended atomically with the contact write and
	// the feed is rung after commit. Nil disables event emission (tests that do
	// not exercise the event plane leave it nil).
	Outbox *outbox.Outbox
}

func NewService(db *sql.DB) *Service {
	return &Service{DB: db, Repo: NewRepo(), Now: time.Now}
}

// CreateContact inserts a contact and any inline emails/phones in one tx.
// The first email and first phone in their respective arrays become primary.
func (s *Service) CreateContact(ctx context.Context, in CreateContactInput) (ContactWithChildren, error) {
	now := s.Now().UTC()
	c := Contact{
		ID:          ids.NewULID(),
		GivenName:   in.GivenName,
		FamilyName:  in.FamilyName,
		DisplayName: in.DisplayName,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ContactWithChildren{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	if err := s.Repo.InsertContact(tx, c); err != nil {
		return ContactWithChildren{}, err
	}
	var emails []Email
	for i, ei := range in.Emails {
		e := Email{
			ID:        ids.NewULID(),
			ContactID: c.ID,
			Email:     ei.Email,
			Label:     ei.Label,
			IsPrimary: i == 0,
			CreatedAt: now,
		}
		if err := s.Repo.InsertEmail(tx, e); err != nil {
			return ContactWithChildren{}, err
		}
		emails = append(emails, e)
	}
	var phones []Phone
	for i, pi := range in.Phones {
		p := Phone{
			ID:        ids.NewULID(),
			ContactID: c.ID,
			Phone:     pi.Phone,
			Label:     pi.Label,
			IsPrimary: i == 0,
			CreatedAt: now,
		}
		if err := s.Repo.InsertPhone(tx, p); err != nil {
			return ContactWithChildren{}, err
		}
		phones = append(phones, p)
	}
	// Atomic outbox (event-protocol.md §4.1): the contact.created event is
	// appended on the SAME tx as the contact write, so the event and the domain
	// change commit together or not at all. Ring() happens AFTER commit (§4.3):
	// the row is not visible to feed readers until then.
	if s.Outbox != nil {
		ev, err := contactCreatedEvent(c, emails, phones)
		if err != nil {
			return ContactWithChildren{}, err
		}
		if err := s.Outbox.Append(tx, ev); err != nil {
			return ContactWithChildren{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return ContactWithChildren{}, fmt.Errorf("commit: %w", err)
	}
	if s.Outbox != nil {
		s.Outbox.Ring()
	}
	return ContactWithChildren{Contact: c, Emails: emails, Phones: phones}, nil
}

// GetContact returns a contact and its live (or all, if includeDeleted) children.
func (s *Service) GetContact(ctx context.Context, id string, includeDeleted bool) (ContactWithChildren, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ContactWithChildren{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	c, err := s.Repo.GetContact(tx, id, includeDeleted)
	if err != nil {
		return ContactWithChildren{}, err
	}
	es, err := s.Repo.ListEmails(tx, id)
	if err != nil {
		return ContactWithChildren{}, err
	}
	ps, err := s.Repo.ListPhones(tx, id)
	if err != nil {
		return ContactWithChildren{}, err
	}
	return ContactWithChildren{Contact: c, Emails: es, Phones: ps}, nil
}

// ListContacts paginates contacts with optional search.
type ListResult struct {
	Items      []ContactWithChildren
	NextCursor string
}

func (s *Service) ListContacts(ctx context.Context, p ListParams) (ListResult, error) {
	tx, err := s.DB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ListResult{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	cs, err := s.Repo.ListContacts(tx, p)
	if err != nil {
		return ListResult{}, err
	}
	items := make([]ContactWithChildren, 0, len(cs))
	for _, c := range cs {
		es, err := s.Repo.ListEmails(tx, c.ID)
		if err != nil {
			return ListResult{}, err
		}
		ps, err := s.Repo.ListPhones(tx, c.ID)
		if err != nil {
			return ListResult{}, err
		}
		items = append(items, ContactWithChildren{Contact: c, Emails: es, Phones: ps})
	}
	var next string
	limit := p.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	if len(items) == limit {
		next = items[len(items)-1].ID
	}
	return ListResult{Items: items, NextCursor: next}, nil
}

// UpdateContact applies a partial update to the name fields.
func (s *Service) UpdateContact(ctx context.Context, id string, in UpdateContactInput) (ContactWithChildren, error) {
	if in.GivenName == nil && in.FamilyName == nil && in.DisplayName == nil {
		return s.GetContact(ctx, id, false)
	}
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return ContactWithChildren{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	if err := s.Repo.UpdateContactNames(tx, id, in.GivenName, in.FamilyName, in.DisplayName, s.Now().UTC()); err != nil {
		return ContactWithChildren{}, err
	}
	c, err := s.Repo.GetContact(tx, id, false)
	if err != nil {
		return ContactWithChildren{}, err
	}
	es, err := s.Repo.ListEmails(tx, id)
	if err != nil {
		return ContactWithChildren{}, err
	}
	ps, err := s.Repo.ListPhones(tx, id)
	if err != nil {
		return ContactWithChildren{}, err
	}
	if err := tx.Commit(); err != nil {
		return ContactWithChildren{}, fmt.Errorf("commit: %w", err)
	}
	return ContactWithChildren{Contact: c, Emails: es, Phones: ps}, nil
}

// DeleteContact soft-deletes the contact and cascades to its children.
func (s *Service) DeleteContact(ctx context.Context, id string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	now := s.Now().UTC()
	if err := s.Repo.SoftDeleteContact(tx, id, now); err != nil {
		return err
	}
	if err := s.Repo.SoftDeleteChildrenOfContact(tx, id, now); err != nil {
		return err
	}
	return tx.Commit()
}

// ── emails ──

func (s *Service) AddEmail(ctx context.Context, contactID string, in EmailInput) (Email, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Email{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	if _, err := s.Repo.GetContact(tx, contactID, false); err != nil {
		return Email{}, err
	}
	n, err := s.Repo.CountLiveEmails(tx, contactID)
	if err != nil {
		return Email{}, fmt.Errorf("count emails: %w", err)
	}
	e := Email{
		ID:        ids.NewULID(),
		ContactID: contactID,
		Email:     in.Email,
		Label:     in.Label,
		IsPrimary: n == 0,
		CreatedAt: s.Now().UTC(),
	}
	if err := s.Repo.InsertEmail(tx, e); err != nil {
		return Email{}, err
	}
	if err := tx.Commit(); err != nil {
		return Email{}, fmt.Errorf("commit: %w", err)
	}
	return e, nil
}

func (s *Service) UpdateEmail(ctx context.Context, contactID, emailID string, in UpdateEmailInput) (Email, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Email{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	target, err := s.Repo.GetEmail(tx, contactID, emailID)
	if err != nil {
		return Email{}, err
	}
	if in.Label != nil {
		if err := s.Repo.SetEmailLabel(tx, emailID, in.Label); err != nil {
			return Email{}, fmt.Errorf("set label: %w", err)
		}
	}
	if in.IsPrimary != nil && *in.IsPrimary && !target.IsPrimary {
		current, err := s.Repo.FindPrimaryEmail(tx, contactID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return Email{}, fmt.Errorf("find primary: %w", err)
		}
		if current != "" {
			if err := s.Repo.DemoteEmail(tx, current); err != nil {
				return Email{}, fmt.Errorf("demote: %w", err)
			}
		}
		if err := s.Repo.PromoteEmail(tx, emailID); err != nil {
			return Email{}, fmt.Errorf("promote: %w", err)
		}
	}
	out, err := s.Repo.GetEmail(tx, contactID, emailID)
	if err != nil {
		return Email{}, err
	}
	if err := tx.Commit(); err != nil {
		return Email{}, fmt.Errorf("commit: %w", err)
	}
	return out, nil
}

func (s *Service) DeleteEmail(ctx context.Context, contactID, emailID string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	if err := s.Repo.SoftDeleteEmail(tx, contactID, emailID, s.Now().UTC()); err != nil {
		return err
	}
	return tx.Commit()
}

// ── phones (mirror of emails) ──

func (s *Service) AddPhone(ctx context.Context, contactID string, in PhoneInput) (Phone, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Phone{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	if _, err := s.Repo.GetContact(tx, contactID, false); err != nil {
		return Phone{}, err
	}
	n, err := s.Repo.CountLivePhones(tx, contactID)
	if err != nil {
		return Phone{}, fmt.Errorf("count phones: %w", err)
	}
	p := Phone{
		ID:        ids.NewULID(),
		ContactID: contactID,
		Phone:     in.Phone,
		Label:     in.Label,
		IsPrimary: n == 0,
		CreatedAt: s.Now().UTC(),
	}
	if err := s.Repo.InsertPhone(tx, p); err != nil {
		return Phone{}, err
	}
	if err := tx.Commit(); err != nil {
		return Phone{}, fmt.Errorf("commit: %w", err)
	}
	return p, nil
}

func (s *Service) UpdatePhone(ctx context.Context, contactID, phoneID string, in UpdatePhoneInput) (Phone, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return Phone{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	target, err := s.Repo.GetPhone(tx, contactID, phoneID)
	if err != nil {
		return Phone{}, err
	}
	if in.Label != nil {
		if err := s.Repo.SetPhoneLabel(tx, phoneID, in.Label); err != nil {
			return Phone{}, fmt.Errorf("set label: %w", err)
		}
	}
	if in.IsPrimary != nil && *in.IsPrimary && !target.IsPrimary {
		current, err := s.Repo.FindPrimaryPhone(tx, contactID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return Phone{}, fmt.Errorf("find primary: %w", err)
		}
		if current != "" {
			if err := s.Repo.DemotePhone(tx, current); err != nil {
				return Phone{}, fmt.Errorf("demote: %w", err)
			}
		}
		if err := s.Repo.PromotePhone(tx, phoneID); err != nil {
			return Phone{}, fmt.Errorf("promote: %w", err)
		}
	}
	out, err := s.Repo.GetPhone(tx, contactID, phoneID)
	if err != nil {
		return Phone{}, err
	}
	if err := tx.Commit(); err != nil {
		return Phone{}, fmt.Errorf("commit: %w", err)
	}
	return out, nil
}

// PurgeResult is returned by CountPurgeable and Purge.
type PurgeResult struct {
	Contacts int64
	Emails   int64
	Phones   int64
}

// CountPurgeable counts rows soft-deleted strictly before cutoff. The
// counts are over the underlying tables independently — a hard purge
// cascades, but for `--dry-run` reporting the operator wants per-table
// granularity. R-CTNS-SDEL names the operation.
func (s *Service) CountPurgeable(ctx context.Context, cutoff time.Time) (PurgeResult, error) {
	ts := cutoff.UTC().Format(time.RFC3339Nano)
	var r PurgeResult
	row := s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM contacts WHERE deleted_at IS NOT NULL AND deleted_at < ?`, ts)
	if err := row.Scan(&r.Contacts); err != nil {
		return PurgeResult{}, fmt.Errorf("count contacts: %w", err)
	}
	row = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM contact_emails WHERE (deleted_at IS NOT NULL AND deleted_at < ?) OR contact_id IN (SELECT id FROM contacts WHERE deleted_at IS NOT NULL AND deleted_at < ?)`, ts, ts)
	if err := row.Scan(&r.Emails); err != nil {
		return PurgeResult{}, fmt.Errorf("count emails: %w", err)
	}
	row = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM contact_phones WHERE (deleted_at IS NOT NULL AND deleted_at < ?) OR contact_id IN (SELECT id FROM contacts WHERE deleted_at IS NOT NULL AND deleted_at < ?)`, ts, ts)
	if err := row.Scan(&r.Phones); err != nil {
		return PurgeResult{}, fmt.Errorf("count phones: %w", err)
	}
	return r, nil
}

// Purge hard-deletes contacts (and their child emails/phones) whose
// `deleted_at` is strictly before cutoff. Free-standing soft-deleted
// children of still-live contacts are also purged. Cascades happen in
// a single transaction (R-CTNS-SDEL).
func (s *Service) Purge(ctx context.Context, cutoff time.Time) (PurgeResult, error) {
	ts := cutoff.UTC().Format(time.RFC3339Nano)
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return PurgeResult{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	// Delete child rows tied to a contact being purged AND any standalone
	// soft-deleted children older than cutoff.
	res, err := tx.ExecContext(ctx, `
		DELETE FROM contact_emails
		WHERE (deleted_at IS NOT NULL AND deleted_at < ?)
		   OR contact_id IN (SELECT id FROM contacts WHERE deleted_at IS NOT NULL AND deleted_at < ?)
	`, ts, ts)
	if err != nil {
		return PurgeResult{}, fmt.Errorf("delete emails: %w", err)
	}
	emails, _ := res.RowsAffected()
	res, err = tx.ExecContext(ctx, `
		DELETE FROM contact_phones
		WHERE (deleted_at IS NOT NULL AND deleted_at < ?)
		   OR contact_id IN (SELECT id FROM contacts WHERE deleted_at IS NOT NULL AND deleted_at < ?)
	`, ts, ts)
	if err != nil {
		return PurgeResult{}, fmt.Errorf("delete phones: %w", err)
	}
	phones, _ := res.RowsAffected()
	res, err = tx.ExecContext(ctx, `DELETE FROM contacts WHERE deleted_at IS NOT NULL AND deleted_at < ?`, ts)
	if err != nil {
		return PurgeResult{}, fmt.Errorf("delete contacts: %w", err)
	}
	contactsN, _ := res.RowsAffected()
	if err := tx.Commit(); err != nil {
		return PurgeResult{}, fmt.Errorf("commit: %w", err)
	}
	return PurgeResult{Contacts: contactsN, Emails: emails, Phones: phones}, nil
}

func (s *Service) DeletePhone(ctx context.Context, contactID, phoneID string) error {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	if err := s.Repo.SoftDeletePhone(tx, contactID, phoneID, s.Now().UTC()); err != nil {
		return err
	}
	return tx.Commit()
}
