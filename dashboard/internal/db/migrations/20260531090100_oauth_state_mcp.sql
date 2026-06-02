-- Extend the login-handshake table (oauth_state) to carry an origin
-- discriminator and the MCP authorize-request context. A 'web' row is the
-- Phase-1 sign-in round-trip (the mcp_* columns stay NULL). An 'mcp' row is a
-- client's /oauth/authorize request riding the same Google login: the columns
-- carry the request context from /oauth/authorize through the callback, where
-- it becomes an authorization code. Existing rows default to 'web'.
ALTER TABLE oauth_state ADD COLUMN origin TEXT NOT NULL DEFAULT 'web';
ALTER TABLE oauth_state ADD COLUMN mcp_client_id TEXT NULL;
ALTER TABLE oauth_state ADD COLUMN mcp_redirect_uri TEXT NULL;
ALTER TABLE oauth_state ADD COLUMN mcp_code_challenge TEXT NULL;
ALTER TABLE oauth_state ADD COLUMN mcp_code_challenge_method TEXT NULL;
ALTER TABLE oauth_state ADD COLUMN mcp_client_state TEXT NULL;
ALTER TABLE oauth_state ADD COLUMN mcp_resource TEXT NULL;
