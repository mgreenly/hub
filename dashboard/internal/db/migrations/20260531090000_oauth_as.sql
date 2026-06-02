-- OAuth authorization-server tables: DCR clients, token chains, access/refresh
-- tokens, authorization codes, and the auth/token/grant audit log. Tokens and
-- codes are stored hashed (SHA-256 hex) — only the hash column is persisted,
-- never the plaintext the client receives.

-- A chain is one grant: a client_id authorized by an owner against one resource.
-- public_id is the opaque, non-time-bearing id shown on user-facing surfaces
-- (the grants list); revoking the chain cascades to all its tokens.
CREATE TABLE oauth_chains (
    id          TEXT PRIMARY KEY,
    public_id   TEXT NOT NULL UNIQUE,
    client_id   TEXT NOT NULL,
    owner_email TEXT NOT NULL,
    resource    TEXT NOT NULL,
    created_at  TEXT NOT NULL,
    revoked_at  TEXT NULL
);
CREATE INDEX idx_oauth_chains_owner_email ON oauth_chains(owner_email);
CREATE INDEX idx_oauth_chains_client_id   ON oauth_chains(client_id);

-- Access and refresh tokens hang off a chain. token_hash is the SHA-256 of the
-- plaintext; used_at marks a refresh token spent (a second use cascade-revokes
-- the chain); revoked_at is set when the chain is revoked.
CREATE TABLE oauth_tokens (
    id          TEXT PRIMARY KEY,
    chain_id    TEXT NOT NULL REFERENCES oauth_chains(id) ON DELETE CASCADE,
    kind        TEXT NOT NULL CHECK (kind IN ('access','refresh')),
    token_hash  TEXT NOT NULL UNIQUE,
    issued_at   TEXT NOT NULL,
    expires_at  TEXT NOT NULL,
    used_at     TEXT NULL,
    revoked_at  TEXT NULL
);
CREATE INDEX idx_oauth_tokens_chain_id ON oauth_tokens(chain_id);

-- Short-lived authorization codes, bound at issue to client_id + PKCE challenge
-- + redirect_uri + resource. On exchange, chain_id is set so a replayed code
-- cascade-revokes the chain it minted.
CREATE TABLE oauth_authcodes (
    id                      TEXT PRIMARY KEY,
    code_hash               TEXT NOT NULL UNIQUE,
    client_id               TEXT NOT NULL,
    owner_email             TEXT NOT NULL,
    code_challenge          TEXT NOT NULL,
    code_challenge_method   TEXT NOT NULL,
    redirect_uri            TEXT NOT NULL,
    resource                TEXT NOT NULL,
    original_state          TEXT NOT NULL,
    issued_at               TEXT NOT NULL,
    expires_at              TEXT NOT NULL,
    used_at                 TEXT NULL,
    chain_id                TEXT NULL
);

-- Dynamic Client Registration records. client_id is the public handle; an
-- unused registration (last_used_at IS NULL) older than a cutoff can be purged.
CREATE TABLE dcr_clients (
    id              TEXT PRIMARY KEY,
    client_id       TEXT NOT NULL UNIQUE,
    client_name     TEXT,
    redirect_uris   TEXT NOT NULL,
    registered_at   TEXT NOT NULL,
    last_used_at    TEXT NULL
);

-- Per-service audit log: auth/token/grant events only. No FK on chain_id so an
-- audit row survives the chain it references.
CREATE TABLE audit_log (
    id            TEXT PRIMARY KEY,
    event_type    TEXT NOT NULL,
    occurred_at   TEXT NOT NULL,
    owner_email   TEXT NULL,
    client_id     TEXT NULL,
    chain_id      TEXT NULL,
    ip            TEXT NULL,
    user_agent    TEXT NULL,
    details       TEXT NULL
);
CREATE INDEX idx_audit_log_occurred_at       ON audit_log(occurred_at);
CREATE INDEX idx_audit_log_owner_occurred_at ON audit_log(owner_email, occurred_at);
