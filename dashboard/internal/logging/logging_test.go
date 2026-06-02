package logging

import (
	"log/slog"
	"testing"
)

func TestParseLevel(t *testing.T) {
	valid := []struct {
		in   string
		want slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"error", slog.LevelError},
		{"DEBUG", slog.LevelDebug},  // case-insensitive
		{"  warn ", slog.LevelWarn}, // trimmed
	}
	for _, tt := range valid {
		got, err := ParseLevel(tt.in)
		if err != nil {
			t.Errorf("ParseLevel(%q): unexpected error: %v", tt.in, err)
			continue
		}
		if got != tt.want {
			t.Errorf("ParseLevel(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}

	invalid := []string{"", "trace", "warning", "1", "info extra"}
	for _, in := range invalid {
		if _, err := ParseLevel(in); err == nil {
			t.Errorf("ParseLevel(%q): want error, got nil", in)
		}
	}
}
