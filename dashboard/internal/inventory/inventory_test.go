package inventory

import (
	"os"
	"path/filepath"
	"testing"
)

// writeManifest creates <root>/<svc>/etc/manifest.env with the given contents.
func writeManifest(t *testing.T, root, svc, contents string) {
	t.Helper()
	dir := filepath.Join(root, svc, "etc")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.env"), []byte(contents), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

// TestReadKeepsOnlyMCP: the dashboard manifest (no MCP) is omitted, a garbled
// manifest is skipped, and only the crm service (MCP=true) is returned.
func TestReadKeepsOnlyMCP(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, "dashboard", "APP=dashboard\nMOUNT=/\nDEFAULT=true\n")
	writeManifest(t, root, "crm", "# crm service\nAPP=crm\nMOUNT=/srv/crm/\nMCP=true\n")
	writeManifest(t, root, "broken", "this is not = = valid\n\x00garbage")

	got, err := Read(root)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d services, want 1: %+v", len(got), got)
	}
	if got[0].Name != "crm" {
		t.Errorf("Name = %q, want crm", got[0].Name)
	}
	if got[0].Mount != "/srv/crm/" {
		t.Errorf("Mount = %q, want /srv/crm/", got[0].Mount)
	}
}
