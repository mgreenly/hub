// Package inventory reads the box's per-service deploy manifests and reports the
// services that expose an MCP endpoint, so the suite plugin's connect skill can
// wire up each one.
//
// A service is included only when its manifest sets MCP=true. The dashboard (the
// authorization server) is intentionally NOT special-cased out: its own manifest
// simply omits MCP=true, so it never appears. If someone mistakenly adds MCP=true
// to the dashboard manifest, it would self-list — the omission is the contract.
package inventory

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Service is one MCP-exposing service discovered on the box. The MCP resource URL
// is not computed here: it needs the request host, which only the HTTP layer has.
type Service struct {
	Name  string
	Mount string
}

// Read globs root/*/etc/manifest.env, parses each as simple shell KEY=value, and
// returns the services whose manifest sets MCP=true (sorted by Name). A single
// unreadable or garbled manifest is skipped, not fatal; the only returned error is
// a glob-level failure.
func Read(root string) ([]Service, error) {
	matches, err := filepath.Glob(filepath.Join(root, "*", "etc", "manifest.env"))
	if err != nil {
		return nil, err
	}
	var services []Service
	for _, path := range matches {
		env, perr := parseManifest(path)
		if perr != nil {
			continue
		}
		if env["MCP"] != "true" {
			continue
		}
		services = append(services, Service{Name: env["APP"], Mount: env["MOUNT"]})
	}
	sort.Slice(services, func(i, j int) bool { return services[i].Name < services[j].Name })
	return services, nil
}

// parseManifest reads a manifest.env into a key/value map: blank lines and lines
// starting with '#' are skipped, each line splits on the first '=', keys and
// values are trimmed, and matching surrounding single or double quotes are
// stripped from the value.
func parseManifest(path string) (map[string]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	env := make(map[string]string)
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		val = stripQuotes(val)
		env[key] = val
	}
	return env, nil
}

// stripQuotes removes a single pair of matching surrounding single or double
// quotes from v, if present.
func stripQuotes(v string) string {
	if len(v) >= 2 {
		if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
			return v[1 : len(v)-1]
		}
	}
	return v
}
