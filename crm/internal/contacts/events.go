package contacts

import (
	"encoding/json"
	"fmt"

	"eventplane/outbox"
)

// eventTimeFormat matches the read API's timestamp rendering (internal/mcp
// tools.go) so the event payload is the same shape the producer's read API
// returns (event-protocol.md §4.4, §8.6).
const eventTimeFormat = "2006-01-02T15:04:05.000000000Z07:00"

// contactCreatedPayload is the §8.6 contact.created snapshot. Field names are the
// wire contract (note: emails/phones use "primary", not the storage layer's
// is_primary). given_name / family_name / label are emitted as null when absent
// so the snapshot is self-describing.
type contactCreatedPayload struct {
	ID          string                `json:"id"`
	DisplayName string                `json:"display_name"`
	GivenName   *string               `json:"given_name"`
	FamilyName  *string               `json:"family_name"`
	Emails      []contactEmailPayload `json:"emails"`
	Phones      []contactPhonePayload `json:"phones"`
	CreatedAt   string                `json:"created_at"`
}

type contactEmailPayload struct {
	Email   string  `json:"email"`
	Label   *string `json:"label"`
	Primary bool    `json:"primary"`
}

type contactPhonePayload struct {
	Phone   string  `json:"phone"`
	Label   *string `json:"label"`
	Primary bool    `json:"primary"`
}

// contactCreatedEvent builds the contact.created outbox event from the freshly
// created aggregate. The library wraps this opaque payload in the uniform
// envelope (§8.3) at serialize time; crm only owns the payload shape.
func contactCreatedEvent(c Contact, emails []Email, phones []Phone) (outbox.Event, error) {
	p := contactCreatedPayload{
		ID:          c.ID,
		DisplayName: c.DisplayName,
		GivenName:   c.GivenName,
		FamilyName:  c.FamilyName,
		Emails:      make([]contactEmailPayload, 0, len(emails)),
		Phones:      make([]contactPhonePayload, 0, len(phones)),
		CreatedAt:   c.CreatedAt.UTC().Format(eventTimeFormat),
	}
	for _, e := range emails {
		p.Emails = append(p.Emails, contactEmailPayload{Email: e.Email, Label: e.Label, Primary: e.IsPrimary})
	}
	for _, ph := range phones {
		p.Phones = append(p.Phones, contactPhonePayload{Phone: ph.Phone, Label: ph.Label, Primary: ph.IsPrimary})
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return outbox.Event{}, fmt.Errorf("marshal contact.created payload: %w", err)
	}
	return outbox.Event{Type: "contact.created", Payload: raw}, nil
}
