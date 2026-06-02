CREATE TABLE oauth_state (
    id                  TEXT PRIMARY KEY,
    binding_cookie_hash TEXT NOT NULL,
    created_at          TEXT NOT NULL,
    expires_at          TEXT NOT NULL
);
