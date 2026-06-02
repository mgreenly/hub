package server

import (
	"context"
	"net/http"
)

// identity is the authenticated caller, as told to us authoritatively by nginx.
type identity struct {
	OwnerEmail string
	ClientID   string
}

type identityCtxKey struct{}

// withIdentity stashes the caller identity on the request context.
func withIdentity(ctx context.Context, id identity) context.Context {
	return context.WithValue(ctx, identityCtxKey{}, id)
}

// identityFrom returns the caller identity on the context, and whether one was
// present. Handlers behind requireIdentityHeaders always get ok == true.
func identityFrom(ctx context.Context) (identity, bool) {
	id, ok := ctx.Value(identityCtxKey{}).(identity)
	return id, ok
}

// requireIdentityHeaders is the trivial replacement for crm.bak's requireBearer:
// it does NO token parsing, NO ValidateAccess, NO hashing. crm performs no token
// logic at all. nginx is the only ingress, the server binds 127.0.0.1, and nginx
// sets X-Owner-Email / X-Client-Id authoritatively only AFTER a successful
// auth_request against the dashboard (clearing any inbound spoof first). So an
// empty X-Owner-Email means the request did not come through the authenticated
// front door (or nginx is misconfigured) — we refuse to serve it.
//
// We trust X-Owner-Email / X-Client-Id precisely because that loopback bind plus
// nginx-only ingress is the entire security boundary; binding a public interface
// would let anyone spoof these headers, which is why main binds 127.0.0.1.
func (a *app) requireIdentityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		owner := r.Header.Get("X-Owner-Email")
		if owner == "" {
			// No authenticated identity: this request did not transit nginx's
			// auth_request gate. The MCP challenge points clients at our PRM doc.
			w.Header().Set("WWW-Authenticate",
				`Bearer resource_metadata="`+a.resourceID+`/.well-known/oauth-protected-resource"`)
			writeJSON(w, http.StatusUnauthorized, map[string]any{
				"error":             "unauthorized",
				"error_description": "missing authenticated identity",
			})
			return
		}
		id := identity{
			OwnerEmail: owner,
			ClientID:   r.Header.Get("X-Client-Id"),
		}
		next.ServeHTTP(w, r.WithContext(withIdentity(r.Context(), id)))
	})
}

// securityHeaders sets transport-hardening headers that don't depend on auth:
// nosniff and no-store on every response, and HSTS only when the request
// arrived over HTTPS (signalled by nginx's X-Forwarded-Proto).
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		if isHTTPS(r) {
			w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
		}
		next.ServeHTTP(w, r)
	})
}

// isHTTPS reports whether the request reached the front door over TLS. nginx
// terminates TLS and forwards the original scheme via X-Forwarded-Proto; on
// plain localhost there is no TLS and no header, so HSTS is correctly omitted.
func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return r.Header.Get("X-Forwarded-Proto") == "https"
}
