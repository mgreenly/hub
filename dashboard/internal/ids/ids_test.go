package ids

import "testing"

func TestNewIsUniqueAndNonEmpty(t *testing.T) {
	seen := make(map[string]bool, 1000)
	for i := 0; i < 1000; i++ {
		id := New()
		if id == "" {
			t.Fatal("New returned empty string")
		}
		if seen[id] {
			t.Fatalf("New returned a duplicate: %q", id)
		}
		seen[id] = true
	}
}

func TestNewLength(t *testing.T) {
	// 16 bytes base32-encoded without padding is 26 characters.
	if got := len(New()); got != 26 {
		t.Fatalf("New() length = %d, want 26", got)
	}
}
