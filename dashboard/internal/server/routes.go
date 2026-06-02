package server

import "net/http"

// routes is the dashboard's complete URL surface. New() builds the *http.Server;
// this is the one place that answers "what does this server expose?".
func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /{$}", a.handleIndex())
	mux.Handle("GET /login", a.handleLogin())
	mux.Handle("GET /oauth/google/callback", a.handleCallback())
	mux.Handle("POST /logout", a.handleLogout())
	mux.Handle("GET /services", a.handleInventory())
	mux.Handle("GET /install", a.handleInstall())
	mux.Handle("GET /static/", a.staticHandler())

	// Live-grants block on the logged-in index: session-authenticated (not
	// bearer / not auth_request / not loopback). SSE stream, the HTML fragment
	// the stream client swaps in, and per-grant web revocation.
	mux.Handle("GET /grants/stream", a.handleGrantsStream())
	mux.Handle("GET /grants/fragment", a.handleGrantsFragment())
	mux.Handle("POST /grants/{public_id}/revoke", a.handleGrantRevoke())

	// OAuth authorization-server surface.
	mux.Handle("GET /.well-known/oauth-authorization-server", a.handleASMetadata())
	mux.Handle("POST /oauth/register", a.handleDCRRegister())
	mux.Handle("GET /oauth/authorize", a.handleAuthorize())
	mux.Handle("POST /oauth/token", a.handleToken())
	mux.Handle("POST /oauth/introspect", a.handleIntrospect())
	mux.Handle("POST /oauth/revoke", a.handleRevoke())

	// Loopback-only token introspection nginx calls via auth_request on every
	// service request. The nginx location is marked `internal;`; the handler
	// re-checks loopback as defense in depth. Registered with no method
	// constraint: nginx's auth_request subrequest mirrors the original request
	// method (GET for a GET to the service, POST for a POST, ...), so pinning a
	// single verb here would 405 every mismatch — which auth_request then turns
	// into a 500. Introspection is method-independent, so accept any method.
	mux.Handle("/internal/authn", a.handleAuthn())

	return securityHeaders(mux)
}
