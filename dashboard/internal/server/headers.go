package server

import "net/http"

// securityHeaders sets transport-hardening headers that don't depend on auth:
// nosniff and no-store on every response, and HSTS only when the request
// arrived over HTTPS. no-store keeps clients and intermediaries from caching
// any response — appropriate while the surface is identity-aware and small.
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

// isHTTPS reports whether the request reached us over TLS, either directly or
// via nginx's X-Forwarded-Proto signal. HSTS must never be sent on plain HTTP,
// so localhost (no TLS, no forwarded header) correctly omits it.
func isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return r.Header.Get("X-Forwarded-Proto") == "https"
}
