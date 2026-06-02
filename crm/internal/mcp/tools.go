package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode"

	"github.com/nyaruka/phonenumbers"

	"crm/internal/contacts"
)

// toolDescriptors returns the crm_* tool set. Each schema is hand-coded; a full
// JSON Schema isn't required by MCP clients but improves the LLM hinting.
func toolDescriptors() []map[string]any {
	return []map[string]any{
		desc("crm_contact_create", "Create a new contact. Optional inline emails and phones are created atomically; the first of each becomes primary.", obj(map[string]any{
			"given_name":   typ("string"),
			"family_name":  typ("string"),
			"display_name": typ("string"),
			"emails":       array(obj(map[string]any{"email": typ("string"), "label": typ("string")}, "email")),
			"phones":       array(obj(map[string]any{"phone": typ("string"), "label": typ("string")}, "phone")),
		})),
		desc("crm_contact_get", "Fetch one contact by ULID, including all emails and phones.", obj(map[string]any{"id": typ("string")}, "id")),
		desc("crm_contact_list", "List or search contacts. 'q' substring-matches name/email/phone. Use 'next_cursor' as 'after_id' to paginate.", obj(map[string]any{
			"q":        typ("string"),
			"limit":    typ("integer"),
			"after_id": typ("string"),
		})),
		desc("crm_contact_update", "Update name fields only. Does not touch emails or phones.", obj(map[string]any{
			"id": typ("string"), "given_name": typ("string"), "family_name": typ("string"), "display_name": typ("string"),
		}, "id")),
		desc("crm_contact_delete", "Soft-delete a contact (children cascade).", obj(map[string]any{"id": typ("string")}, "id")),

		desc("crm_contact_email_add", "Add an email to a contact. First email becomes primary automatically.", obj(map[string]any{
			"contact_id": typ("string"), "email": typ("string"), "label": typ("string"),
		}, "contact_id", "email")),
		desc("crm_contact_email_update", "Update an email's label or primary status. Promoting auto-demotes the current primary.", obj(map[string]any{
			"contact_id": typ("string"), "email_id": typ("string"), "label": typ("string"), "is_primary": typ("boolean"),
		}, "contact_id", "email_id")),
		desc("crm_contact_email_delete", "Remove an email.", obj(map[string]any{"contact_id": typ("string"), "email_id": typ("string")}, "contact_id", "email_id")),

		desc("crm_contact_phone_add", "Add a phone to a contact. First phone becomes primary automatically. Phone numbers MUST be fully-qualified E.164 (no default region) — format on your side.", obj(map[string]any{
			"contact_id": typ("string"), "phone": typ("string"), "label": typ("string"),
		}, "contact_id", "phone")),
		desc("crm_contact_phone_update", "Update a phone's label or primary status. Promoting auto-demotes the current primary.", obj(map[string]any{
			"contact_id": typ("string"), "phone_id": typ("string"), "label": typ("string"), "is_primary": typ("boolean"),
		}, "contact_id", "phone_id")),
		desc("crm_contact_phone_delete", "Remove a phone.", obj(map[string]any{"contact_id": typ("string"), "phone_id": typ("string")}, "contact_id", "phone_id")),

		desc("crm_whoami", "Return the authenticated caller's identity (owner email and client id) as established by the platform's auth gate. Takes no inputs; the end-to-end auth proof.", obj(map[string]any{})),
	}
}

func desc(name, description string, schema map[string]any) map[string]any {
	return map[string]any{"name": name, "description": description, "inputSchema": schema}
}

func obj(props map[string]any, required ...string) map[string]any {
	o := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		o["required"] = required
	}
	return o
}

func typ(t string) map[string]any { return map[string]any{"type": t} }
func array(items map[string]any) map[string]any {
	return map[string]any{"type": "array", "items": items}
}

// ── dispatch ──────────────────────────────────────────────────────────────

type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (h *Handler) handleToolCall(ctx context.Context, w http.ResponseWriter, req jsonRPCRequest, id Identity) {
	var p toolCallParams
	if err := json.Unmarshal(req.Params, &p); err != nil {
		writeJSONRPCError(w, req.ID, -32602, "invalid params")
		return
	}
	res, err := dispatchTool(ctx, h.contacts, p.Name, p.Arguments, id)
	if err != nil {
		writeJSONRPCResult(w, req.ID, toolResultErr(err.Error()))
		return
	}
	writeJSONRPCResult(w, req.ID, res)
}

func dispatchTool(ctx context.Context, svc *contacts.Service, name string, argsRaw json.RawMessage, id Identity) (map[string]any, error) {
	switch name {
	case "crm_contact_create":
		return toolContactCreate(ctx, svc, argsRaw)
	case "crm_contact_get":
		return toolContactGet(ctx, svc, argsRaw)
	case "crm_contact_list":
		return toolContactList(ctx, svc, argsRaw)
	case "crm_contact_update":
		return toolContactUpdate(ctx, svc, argsRaw)
	case "crm_contact_delete":
		return toolContactDelete(ctx, svc, argsRaw)
	case "crm_contact_email_add":
		return toolEmailAdd(ctx, svc, argsRaw)
	case "crm_contact_email_update":
		return toolEmailUpdate(ctx, svc, argsRaw)
	case "crm_contact_email_delete":
		return toolEmailDelete(ctx, svc, argsRaw)
	case "crm_contact_phone_add":
		return toolPhoneAdd(ctx, svc, argsRaw)
	case "crm_contact_phone_update":
		return toolPhoneUpdate(ctx, svc, argsRaw)
	case "crm_contact_phone_delete":
		return toolPhoneDelete(ctx, svc, argsRaw)
	case "crm_whoami":
		return toolWhoami(id)
	default:
		return nil, errors.New("unknown tool: " + name)
	}
}

// ── tool implementations ─────────────────────────────────────────────────

func toolWhoami(id Identity) (map[string]any, error) {
	return toolResultJSON(map[string]any{
		"owner_email": id.OwnerEmail,
		"client_id":   id.ClientID,
	})
}

type emailArg struct {
	Email string  `json:"email"`
	Label *string `json:"label,omitempty"`
}
type phoneArg struct {
	Phone string  `json:"phone"`
	Label *string `json:"label,omitempty"`
}

func toolContactCreate(ctx context.Context, svc *contacts.Service, raw json.RawMessage) (map[string]any, error) {
	var args struct {
		GivenName   *string    `json:"given_name,omitempty"`
		FamilyName  *string    `json:"family_name,omitempty"`
		DisplayName *string    `json:"display_name,omitempty"`
		Emails      []emailArg `json:"emails,omitempty"`
		Phones      []phoneArg `json:"phones,omitempty"`
	}
	if err := json.Unmarshal(raw, &args); err != nil {
		return nil, err
	}
	emails := make([]contacts.EmailInput, 0, len(args.Emails))
	for _, e := range args.Emails {
		norm, err := normalizeEmail(e.Email)
		if err != nil {
			return toolResultErr(err.Error()), nil
		}
		if err := validateLabel(e.Label); err != nil {
			return toolResultErr(err.Error()), nil
		}
		emails = append(emails, contacts.EmailInput{Email: norm, Label: e.Label})
	}
	phones := make([]contacts.PhoneInput, 0, len(args.Phones))
	for _, p := range args.Phones {
		norm, err := normalizePhone(p.Phone)
		if err != nil {
			return toolResultErr(err.Error()), nil
		}
		if err := validateLabel(p.Label); err != nil {
			return toolResultErr(err.Error()), nil
		}
		phones = append(phones, contacts.PhoneInput{Phone: norm, Label: p.Label})
	}
	display, err := deriveDisplayName(args.DisplayName, args.GivenName, args.FamilyName, emails)
	if err != nil {
		return toolResultErr(err.Error()), nil
	}
	out, err := svc.CreateContact(ctx, contacts.CreateContactInput{
		GivenName: trimPtr(args.GivenName), FamilyName: trimPtr(args.FamilyName),
		DisplayName: display, Emails: emails, Phones: phones,
	})
	if err != nil {
		return toolResultErr(translateContactsError(err)), nil
	}
	return toolResultJSON(contactJSON(out))
}

func toolContactGet(ctx context.Context, svc *contacts.Service, raw json.RawMessage) (map[string]any, error) {
	var a struct {
		ID             string `json:"id"`
		IncludeDeleted bool   `json:"include_deleted,omitempty"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	out, err := svc.GetContact(ctx, a.ID, a.IncludeDeleted)
	if err != nil {
		return toolResultErr(translateContactsError(err)), nil
	}
	return toolResultJSON(contactJSON(out))
}

func toolContactList(ctx context.Context, svc *contacts.Service, raw json.RawMessage) (map[string]any, error) {
	var a struct {
		Q              string `json:"q,omitempty"`
		Limit          int    `json:"limit,omitempty"`
		AfterID        string `json:"after_id,omitempty"`
		IncludeDeleted bool   `json:"include_deleted,omitempty"`
	}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &a); err != nil {
			return nil, err
		}
	}
	if a.Limit <= 0 {
		a.Limit = 50
	}
	out, err := svc.ListContacts(ctx, contacts.ListParams{
		Q: strings.TrimSpace(a.Q), Limit: a.Limit, AfterID: a.AfterID, IncludeDeleted: a.IncludeDeleted,
	})
	if err != nil {
		return toolResultErr(translateContactsError(err)), nil
	}
	items := make([]map[string]any, len(out.Items))
	for i, c := range out.Items {
		items[i] = contactJSON(c)
	}
	return toolResultJSON(map[string]any{"items": items, "next_cursor": out.NextCursor})
}

func toolContactUpdate(ctx context.Context, svc *contacts.Service, raw json.RawMessage) (map[string]any, error) {
	var a struct {
		ID          string  `json:"id"`
		GivenName   *string `json:"given_name,omitempty"`
		FamilyName  *string `json:"family_name,omitempty"`
		DisplayName *string `json:"display_name,omitempty"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	out, err := svc.UpdateContact(ctx, a.ID, contacts.UpdateContactInput{
		GivenName: trimPtr(a.GivenName), FamilyName: trimPtr(a.FamilyName), DisplayName: trimPtr(a.DisplayName),
	})
	if err != nil {
		return toolResultErr(translateContactsError(err)), nil
	}
	return toolResultJSON(contactJSON(out))
}

func toolContactDelete(ctx context.Context, svc *contacts.Service, raw json.RawMessage) (map[string]any, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	if err := svc.DeleteContact(ctx, a.ID); err != nil {
		return toolResultErr(translateContactsError(err)), nil
	}
	return toolResultJSON(map[string]any{"ok": true})
}

func toolEmailAdd(ctx context.Context, svc *contacts.Service, raw json.RawMessage) (map[string]any, error) {
	var a struct {
		ContactID string  `json:"contact_id"`
		Email     string  `json:"email"`
		Label     *string `json:"label,omitempty"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	norm, err := normalizeEmail(a.Email)
	if err != nil {
		return toolResultErr(err.Error()), nil
	}
	if err := validateLabel(a.Label); err != nil {
		return toolResultErr(err.Error()), nil
	}
	out, err := svc.AddEmail(ctx, a.ContactID, contacts.EmailInput{Email: norm, Label: a.Label})
	if err != nil {
		return toolResultErr(translateContactsError(err)), nil
	}
	return toolResultJSON(emailJSON(out))
}

func toolEmailUpdate(ctx context.Context, svc *contacts.Service, raw json.RawMessage) (map[string]any, error) {
	var a struct {
		ContactID string  `json:"contact_id"`
		EmailID   string  `json:"email_id"`
		Label     *string `json:"label,omitempty"`
		IsPrimary *bool   `json:"is_primary,omitempty"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	if a.Label != nil {
		if err := validateLabel(a.Label); err != nil {
			return toolResultErr(err.Error()), nil
		}
	}
	out, err := svc.UpdateEmail(ctx, a.ContactID, a.EmailID, contacts.UpdateEmailInput{Label: a.Label, IsPrimary: a.IsPrimary})
	if err != nil {
		return toolResultErr(translateContactsError(err)), nil
	}
	return toolResultJSON(emailJSON(out))
}

func toolEmailDelete(ctx context.Context, svc *contacts.Service, raw json.RawMessage) (map[string]any, error) {
	var a struct {
		ContactID string `json:"contact_id"`
		EmailID   string `json:"email_id"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	if err := svc.DeleteEmail(ctx, a.ContactID, a.EmailID); err != nil {
		return toolResultErr(translateContactsError(err)), nil
	}
	return toolResultJSON(map[string]any{"ok": true})
}

func toolPhoneAdd(ctx context.Context, svc *contacts.Service, raw json.RawMessage) (map[string]any, error) {
	var a struct {
		ContactID string  `json:"contact_id"`
		Phone     string  `json:"phone"`
		Label     *string `json:"label,omitempty"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	norm, err := normalizePhone(a.Phone)
	if err != nil {
		return toolResultErr(err.Error()), nil
	}
	if err := validateLabel(a.Label); err != nil {
		return toolResultErr(err.Error()), nil
	}
	out, err := svc.AddPhone(ctx, a.ContactID, contacts.PhoneInput{Phone: norm, Label: a.Label})
	if err != nil {
		return toolResultErr(translateContactsError(err)), nil
	}
	return toolResultJSON(phoneJSON(out))
}

func toolPhoneUpdate(ctx context.Context, svc *contacts.Service, raw json.RawMessage) (map[string]any, error) {
	var a struct {
		ContactID string  `json:"contact_id"`
		PhoneID   string  `json:"phone_id"`
		Label     *string `json:"label,omitempty"`
		IsPrimary *bool   `json:"is_primary,omitempty"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	if a.Label != nil {
		if err := validateLabel(a.Label); err != nil {
			return toolResultErr(err.Error()), nil
		}
	}
	out, err := svc.UpdatePhone(ctx, a.ContactID, a.PhoneID, contacts.UpdatePhoneInput{Label: a.Label, IsPrimary: a.IsPrimary})
	if err != nil {
		return toolResultErr(translateContactsError(err)), nil
	}
	return toolResultJSON(phoneJSON(out))
}

func toolPhoneDelete(ctx context.Context, svc *contacts.Service, raw json.RawMessage) (map[string]any, error) {
	var a struct {
		ContactID string `json:"contact_id"`
		PhoneID   string `json:"phone_id"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return nil, err
	}
	if err := svc.DeletePhone(ctx, a.ContactID, a.PhoneID); err != nil {
		return toolResultErr(translateContactsError(err)), nil
	}
	return toolResultJSON(map[string]any{"ok": true})
}

// ── shared helpers ──────────────────────────────────────────────────────

func toolResultJSON(v any) (map[string]any, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return toolResultText(string(b)), nil
}

func contactJSON(c contacts.ContactWithChildren) map[string]any {
	out := map[string]any{
		"id":           c.ID,
		"display_name": c.DisplayName,
		"created_at":   c.CreatedAt.UTC().Format("2006-01-02T15:04:05.000000000Z07:00"),
		"updated_at":   c.UpdatedAt.UTC().Format("2006-01-02T15:04:05.000000000Z07:00"),
	}
	if c.GivenName != nil {
		out["given_name"] = *c.GivenName
	}
	if c.FamilyName != nil {
		out["family_name"] = *c.FamilyName
	}
	emails := make([]map[string]any, len(c.Emails))
	for i, e := range c.Emails {
		emails[i] = emailJSON(e)
	}
	out["emails"] = emails
	phones := make([]map[string]any, len(c.Phones))
	for i, p := range c.Phones {
		phones[i] = phoneJSON(p)
	}
	out["phones"] = phones
	if c.DeletedAt != nil {
		out["deleted_at"] = c.DeletedAt.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
	}
	return out
}

func emailJSON(e contacts.Email) map[string]any {
	out := map[string]any{
		"id": e.ID, "email": e.Email, "is_primary": e.IsPrimary,
		"created_at": e.CreatedAt.UTC().Format("2006-01-02T15:04:05.000000000Z07:00"),
	}
	if e.Label != nil {
		out["label"] = *e.Label
	}
	if e.DeletedAt != nil {
		out["deleted_at"] = e.DeletedAt.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
	}
	return out
}

func phoneJSON(p contacts.Phone) map[string]any {
	out := map[string]any{
		"id": p.ID, "phone": p.Phone, "is_primary": p.IsPrimary,
		"created_at": p.CreatedAt.UTC().Format("2006-01-02T15:04:05.000000000Z07:00"),
	}
	if p.Label != nil {
		out["label"] = *p.Label
	}
	if p.DeletedAt != nil {
		out["deleted_at"] = p.DeletedAt.UTC().Format("2006-01-02T15:04:05.000000000Z07:00")
	}
	return out
}

// Mirrors of server-layer normalization — kept in sync with the contacts domain.

func normalizeEmail(in string) (string, error) {
	s := strings.ToLower(strings.TrimSpace(in))
	if s == "" {
		return "", errors.New("email must not be empty")
	}
	at := strings.IndexByte(s, '@')
	if at <= 0 || at == len(s)-1 {
		return "", errors.New("email must contain a local-part and a domain")
	}
	return s, nil
}

func normalizePhone(in string) (string, error) {
	s := strings.TrimSpace(in)
	if s == "" {
		return "", errors.New("phone must not be empty")
	}
	if !strings.HasPrefix(s, "+") {
		return "", errors.New("phone must be fully qualified E.164 starting with '+'")
	}
	parsed, err := phonenumbers.Parse(s, "")
	if err != nil {
		return "", errors.New("phone is not a valid E.164 number")
	}
	if !phonenumbers.IsValidNumber(parsed) {
		return "", errors.New("phone is not a valid E.164 number")
	}
	return phonenumbers.Format(parsed, phonenumbers.E164), nil
}

func validateLabel(label *string) error {
	if label == nil {
		return nil
	}
	if len(*label) > 40 {
		return errors.New("label must be at most 40 characters")
	}
	for _, r := range *label {
		if unicode.IsControl(r) {
			return errors.New("label must not contain control characters")
		}
	}
	return nil
}

func deriveDisplayName(supplied, given, family *string, emails []contacts.EmailInput) (string, error) {
	if s := ptrTrim(supplied); s != "" {
		return s, nil
	}
	g := ptrTrim(given)
	f := ptrTrim(family)
	combined := strings.TrimSpace(g + " " + f)
	if combined != "" {
		return combined, nil
	}
	if len(emails) > 0 && emails[0].Email != "" {
		return emails[0].Email, nil
	}
	return "", errors.New("contact must have at least one identifying field")
}

func ptrTrim(s *string) string {
	if s == nil {
		return ""
	}
	return strings.TrimSpace(*s)
}

func trimPtr(s *string) *string {
	if s == nil {
		return nil
	}
	v := strings.TrimSpace(*s)
	return &v
}
