CREATE TABLE web_sessions (
    id           TEXT PRIMARY KEY,
    owner_email  TEXT NOT NULL,
    cookie_hash  TEXT NOT NULL UNIQUE,
    issued_at    TEXT NOT NULL,
    expires_at   TEXT NOT NULL,
    last_seen_at TEXT NOT NULL,
    revoked_at   TEXT NULL
);
CREATE INDEX idx_web_sessions_owner_email ON web_sessions(owner_email);
