package server

import (
	"fmt"
	"net/http"
	"strings"

	"dashboard/internal/inventory"
)

// installScript builds the bash script served at GET /install — the target of
// the landing page's `curl -fsSL https://<host>/install | bash` one-paste. For
// every MCP-exposing service on the box it emits a remove-then-add pair at user
// scope, self-templated to the caller's host. The remove (`|| true`) makes a
// re-run authoritative even though `claude mcp add` alone errors on a duplicate
// name; `set -e` still aborts loudly on a real failure (missing claude, network).
func installScript(scheme, host string, svcs []inventory.Service) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("set -eu\n\n")
	for _, s := range svcs {
		resource := mcpResourceURL(scheme, host, s.Mount)
		fmt.Fprintf(&b, "claude mcp remove --scope user %s >/dev/null 2>&1 || true\n", s.Name)
		fmt.Fprintf(&b, "claude mcp add --scope user --transport http %s %s\n\n", s.Name, resource)
	}
	b.WriteString(`echo "Done. Restart Claude Code for the new MCP servers to load."` + "\n")
	return b.String()
}

// handleInstall serves GET /install: a public (no-auth) bash script that wires
// the box's MCP services into a Claude Code install via `claude mcp add`. Public
// for the same reason as /services — it only emits the box's own resource URLs,
// which are themselves OAuth-protected. text/plain so curl gets verbatim bytes.
func (a *app) handleInstall() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		svcs, err := inventory.Read(a.manifestRoot)
		if err != nil {
			a.logger.Error("install.read_inventory", "err", err)
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		script := installScript(requestScheme(r), r.Host, svcs)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(script))
	}
}
