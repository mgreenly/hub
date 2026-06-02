package server

import "net/http"

// handleLogout ends a web session. It is POST-only (wired in routes) so a
// prefetched or crawled link can never sign a user out: revoking is a state
// change and belongs behind a form submission. It reads the dashboard_session
// cookie, revokes the matching session (idempotent — a second logout or an
// unknown cookie is a no-op), clears the cookie, and redirects to /.
func (a *app) handleLogout() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(sessionCookieName); err == nil {
			if err := a.sessions.Revoke(r.Context(), c.Value); err != nil {
				a.logger.Error("logout.revoke", "err", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
				return
			}
		}
		clearSessionCookie(w, r)
		http.Redirect(w, r, "/", http.StatusFound)
	}
}
