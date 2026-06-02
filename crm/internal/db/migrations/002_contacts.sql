-- R-CTNS-SDEL, R-CTNS-PRIM, R-CTNS-EMNM, R-CTNS-PHFM, R-CTNS-LBLF: contacts domain.

CREATE TABLE contacts (
    id            TEXT PRIMARY KEY,
    given_name    TEXT NULL,
    family_name   TEXT NULL,
    display_name  TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL,
    deleted_at    TEXT NULL
);
CREATE INDEX idx_contacts_display_name_nocase
    ON contacts(display_name COLLATE NOCASE);
CREATE INDEX idx_contacts_live
    ON contacts(id) WHERE deleted_at IS NULL;

CREATE TABLE contact_emails (
    id          TEXT PRIMARY KEY,
    contact_id  TEXT NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
    email       TEXT NOT NULL,
    label       TEXT NULL,
    is_primary  INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL,
    deleted_at  TEXT NULL
);
CREATE INDEX idx_contact_emails_contact_live
    ON contact_emails(contact_id) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX uq_contact_emails_contact_email_live
    ON contact_emails(contact_id, email) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX uq_contact_emails_primary_live
    ON contact_emails(contact_id) WHERE is_primary = 1 AND deleted_at IS NULL;

CREATE TABLE contact_phones (
    id          TEXT PRIMARY KEY,
    contact_id  TEXT NOT NULL REFERENCES contacts(id) ON DELETE CASCADE,
    phone       TEXT NOT NULL,
    label       TEXT NULL,
    is_primary  INTEGER NOT NULL DEFAULT 0,
    created_at  TEXT NOT NULL,
    deleted_at  TEXT NULL
);
CREATE INDEX idx_contact_phones_contact_live
    ON contact_phones(contact_id) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX uq_contact_phones_contact_phone_live
    ON contact_phones(contact_id, phone) WHERE deleted_at IS NULL;
CREATE UNIQUE INDEX uq_contact_phones_primary_live
    ON contact_phones(contact_id) WHERE is_primary = 1 AND deleted_at IS NULL;
