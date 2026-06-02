package contacts

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"eventplane/outbox"
)

// TestCreateContact_EmitsContactCreated proves the crm→eventplane integration:
// creating a contact appends exactly one contact.created event, atomically, with
// the §8.6 payload shape (note the wire field names email/phone/primary).
func TestCreateContact_EmitsContactCreated(t *testing.T) {
	conn := openDB(t)
	ob, err := outbox.New(conn, outbox.Options{Source: "crm"}) // empty DBPath skips the file probe
	if err != nil {
		t.Fatalf("outbox.New: %v", err)
	}
	s := NewService(conn)
	s.Now = func() time.Time { return time.Date(2026, 6, 2, 15, 4, 5, 123000000, time.UTC) }
	s.Outbox = ob

	out, err := s.CreateContact(context.Background(), CreateContactInput{
		DisplayName: "Jane Doe",
		GivenName:   ptr("Jane"),
		FamilyName:  ptr("Doe"),
		Emails:      []EmailInput{{Email: "jane@example.com", Label: ptr("work")}},
		Phones:      []PhoneInput{{Phone: "+15551234567", Label: ptr("mobile")}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// Exactly one row, of the right type.
	var typ, payload string
	var n int
	if err := conn.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM outbox`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 1 {
		t.Fatalf("outbox rows: got %d want 1", n)
	}
	if err := conn.QueryRowContext(context.Background(),
		`SELECT type, payload FROM outbox WHERE seq = 1`).Scan(&typ, &payload); err != nil {
		t.Fatalf("select: %v", err)
	}
	if typ != "contact.created" {
		t.Fatalf("type: got %q want contact.created", typ)
	}

	var p map[string]any
	if err := json.Unmarshal([]byte(payload), &p); err != nil {
		t.Fatalf("payload not JSON: %v", err)
	}
	if p["id"] != out.ID {
		t.Errorf("payload id = %v, want %v", p["id"], out.ID)
	}
	if p["display_name"] != "Jane Doe" || p["given_name"] != "Jane" || p["family_name"] != "Doe" {
		t.Errorf("payload name fields wrong: %v", p)
	}
	if p["created_at"] != "2026-06-02T15:04:05.123000000Z" {
		t.Errorf("payload created_at = %v", p["created_at"])
	}
	emails, _ := p["emails"].([]any)
	if len(emails) != 1 {
		t.Fatalf("emails: %v", p["emails"])
	}
	e0 := emails[0].(map[string]any)
	if e0["email"] != "jane@example.com" || e0["label"] != "work" || e0["primary"] != true {
		t.Errorf("email payload wrong (want email/label/primary fields): %v", e0)
	}
	phones, _ := p["phones"].([]any)
	if len(phones) != 1 {
		t.Fatalf("phones: %v", p["phones"])
	}
	ph0 := phones[0].(map[string]any)
	if ph0["phone"] != "+15551234567" || ph0["label"] != "mobile" || ph0["primary"] != true {
		t.Errorf("phone payload wrong (want phone/label/primary fields): %v", ph0)
	}
}

// TestCreateContact_NoOutboxIsNoop confirms a nil Outbox leaves CreateContact
// behaving exactly as before (event emission is opt-in).
func TestCreateContact_NoOutboxIsNoop(t *testing.T) {
	s := mkSvc(t)
	if _, err := s.CreateContact(context.Background(), CreateContactInput{DisplayName: "No Events"}); err != nil {
		t.Fatalf("create: %v", err)
	}
}
