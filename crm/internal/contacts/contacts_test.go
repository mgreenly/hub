package contacts

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"crm/internal/db"

	_ "modernc.org/sqlite"
)

func openDB(t *testing.T) *sql.DB {
	t.Helper()
	conn, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := conn.Exec(`PRAGMA foreign_keys = ON`); err != nil {
		t.Fatalf("fk pragma: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := db.Migrate(context.Background(), conn); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return conn
}

func mkSvc(t *testing.T) *Service {
	conn := openDB(t)
	s := NewService(conn)
	s.Now = func() time.Time { return time.Now().UTC() }
	return s
}

func ptr(s string) *string { return &s }

func TestCreateContact_InlineEmailsAndPhones_FirstIsPrimary(t *testing.T) {
	s := mkSvc(t)
	out, err := s.CreateContact(context.Background(), CreateContactInput{
		DisplayName: "Alice Adams",
		Emails: []EmailInput{
			{Email: "alice@example.com"},
			{Email: "alice2@example.com"},
		},
		Phones: []PhoneInput{
			{Phone: "+14155552671"},
			{Phone: "+442071838750"},
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if len(out.Emails) != 2 {
		t.Fatalf("emails: got %d want 2", len(out.Emails))
	}
	if !out.Emails[0].IsPrimary || out.Emails[1].IsPrimary {
		t.Errorf("primary email rule violated: %+v", out.Emails)
	}
	if !out.Phones[0].IsPrimary || out.Phones[1].IsPrimary {
		t.Errorf("primary phone rule violated: %+v", out.Phones)
	}
}

func TestUpdateEmail_PromotionAutoDemotes(t *testing.T) {
	s := mkSvc(t)
	c, err := s.CreateContact(context.Background(), CreateContactInput{
		DisplayName: "Bob",
		Emails: []EmailInput{
			{Email: "bob@example.com"},
			{Email: "bob2@example.com"},
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	tru := true
	if _, err := s.UpdateEmail(context.Background(), c.ID, c.Emails[1].ID, UpdateEmailInput{IsPrimary: &tru}); err != nil {
		t.Fatalf("promote: %v", err)
	}
	got, err := s.GetContact(context.Background(), c.ID, false)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	primaryCount := 0
	for _, e := range got.Emails {
		if e.IsPrimary {
			primaryCount++
		}
	}
	if primaryCount != 1 {
		t.Fatalf("expected exactly one primary, got %d", primaryCount)
	}
	// new primary is the previously-non-primary
	for _, e := range got.Emails {
		if e.Email == "bob2@example.com" && !e.IsPrimary {
			t.Errorf("expected bob2 to be promoted")
		}
		if e.Email == "bob@example.com" && e.IsPrimary {
			t.Errorf("expected bob to be demoted")
		}
	}
}

func TestSoftDelete_HidesContactAndChildrenByDefault(t *testing.T) {
	s := mkSvc(t)
	c, err := s.CreateContact(context.Background(), CreateContactInput{
		DisplayName: "Carol",
		Emails:      []EmailInput{{Email: "carol@example.com"}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := s.DeleteContact(context.Background(), c.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := s.GetContact(context.Background(), c.ID, false); !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound on default get, got %v", err)
	}
	got, err := s.GetContact(context.Background(), c.ID, true)
	if err != nil {
		t.Fatalf("get include_deleted: %v", err)
	}
	if got.DeletedAt == nil {
		t.Errorf("expected deleted_at to be set on parent")
	}
	if len(got.Emails) != 0 {
		t.Errorf("expected child emails to be hidden (live only) after cascade, got %d", len(got.Emails))
	}
}

func TestUniqueEmailPerContact_ReturnsConflict(t *testing.T) {
	s := mkSvc(t)
	c, err := s.CreateContact(context.Background(), CreateContactInput{
		DisplayName: "Dan",
		Emails:      []EmailInput{{Email: "dan@example.com"}},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := s.AddEmail(context.Background(), c.ID, EmailInput{Email: "dan@example.com"}); !errors.Is(err, ErrConflict) {
		t.Errorf("expected ErrConflict, got %v", err)
	}
}

func TestList_Pagination_AfterID(t *testing.T) {
	s := mkSvc(t)
	for i := 0; i < 5; i++ {
		if _, err := s.CreateContact(context.Background(), CreateContactInput{DisplayName: "X"}); err != nil {
			t.Fatalf("create %d: %v", i, err)
		}
	}
	r, err := s.ListContacts(context.Background(), ListParams{Limit: 2})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(r.Items) != 2 || r.NextCursor == "" {
		t.Fatalf("page 1: got %d items / cursor=%q", len(r.Items), r.NextCursor)
	}
	r2, err := s.ListContacts(context.Background(), ListParams{Limit: 2, AfterID: r.NextCursor})
	if err != nil {
		t.Fatalf("list 2: %v", err)
	}
	if len(r2.Items) != 2 {
		t.Fatalf("page 2: got %d", len(r2.Items))
	}
	if r2.Items[0].ID <= r.Items[1].ID {
		t.Errorf("pagination not strictly increasing")
	}
}

func TestSearch_AcrossNameAndChildren(t *testing.T) {
	s := mkSvc(t)
	if _, err := s.CreateContact(context.Background(), CreateContactInput{
		DisplayName: "Eve Engineer",
		Emails:      []EmailInput{{Email: "eve@example.com"}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateContact(context.Background(), CreateContactInput{
		DisplayName: "Frank Foo",
		Phones:      []PhoneInput{{Phone: "+442071838750"}},
	}); err != nil {
		t.Fatal(err)
	}
	r, _ := s.ListContacts(context.Background(), ListParams{Q: "eve"})
	if len(r.Items) != 1 || r.Items[0].DisplayName != "Eve Engineer" {
		t.Errorf("name search: %+v", r.Items)
	}
	r2, _ := s.ListContacts(context.Background(), ListParams{Q: "2071838750"})
	if len(r2.Items) != 1 || r2.Items[0].DisplayName != "Frank Foo" {
		t.Errorf("phone search: %+v", r2.Items)
	}
}

func TestDeletePrimary_LeavesNoPrimary(t *testing.T) {
	s := mkSvc(t)
	c, err := s.CreateContact(context.Background(), CreateContactInput{
		DisplayName: "Gina",
		Emails: []EmailInput{
			{Email: "g1@example.com"},
			{Email: "g2@example.com"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	primaryID := c.Emails[0].ID
	if err := s.DeleteEmail(context.Background(), c.ID, primaryID); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetContact(context.Background(), c.ID, false)
	for _, e := range got.Emails {
		if e.IsPrimary {
			t.Errorf("expected no primary after deleting it, got %v", e)
		}
	}
}
