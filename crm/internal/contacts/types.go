// Package contacts is the contact-domain repo and service per
// docs/design/contacts.md. Repo is SQL-only; Service owns transactions and
// the primary/soft-delete rules. Normalization (email lowercase, phone E.164,
// display_name derivation, label validation) lives in the handler layer per
// R-CTNS-NRMZ.
package contacts

import (
	"errors"
	"time"
)

// Error sentinels mapped onto the structured error vocabulary R-CTNS-ERRC
// names. Translation to wire shape happens in the handler.
var (
	ErrNotFound   = errors.New("contacts: not found")
	ErrConflict   = errors.New("contacts: conflict")
	ErrNoPrimary  = errors.New("contacts: no primary defined")
	ErrValidation = errors.New("contacts: validation")
)

// Contact mirrors a row from contacts.
type Contact struct {
	ID          string
	GivenName   *string
	FamilyName  *string
	DisplayName string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	DeletedAt   *time.Time
}

// Email mirrors a row from contact_emails.
type Email struct {
	ID        string
	ContactID string
	Email     string
	Label     *string
	IsPrimary bool
	CreatedAt time.Time
	DeletedAt *time.Time
}

// Phone mirrors a row from contact_phones.
type Phone struct {
	ID        string
	ContactID string
	Phone     string
	Label     *string
	IsPrimary bool
	CreatedAt time.Time
	DeletedAt *time.Time
}

// ContactWithChildren is the typical read shape.
type ContactWithChildren struct {
	Contact
	Emails []Email
	Phones []Phone
}

// EmailInput is what the service receives for an inline-create or add.
type EmailInput struct {
	Email string
	Label *string
}

// PhoneInput is what the service receives for an inline-create or add.
type PhoneInput struct {
	Phone string
	Label *string
}

// CreateContactInput is what the service receives for POST /contacts.
type CreateContactInput struct {
	GivenName   *string
	FamilyName  *string
	DisplayName string
	Emails      []EmailInput
	Phones      []PhoneInput
}

// UpdateContactInput is what the service receives for PATCH /contacts/{id}.
// Pointer means "field provided" — a non-nil pointer to "" is a valid
// distinct value (validated by the handler).
type UpdateContactInput struct {
	GivenName   *string
	FamilyName  *string
	DisplayName *string
}

// UpdateEmailInput / UpdatePhoneInput drive PATCH on a child row.
type UpdateEmailInput struct {
	Label     *string
	IsPrimary *bool
}

type UpdatePhoneInput struct {
	Label     *string
	IsPrimary *bool
}

// ListParams drive GET /contacts.
type ListParams struct {
	Q              string
	Limit          int
	AfterID        string
	IncludeDeleted bool
}
