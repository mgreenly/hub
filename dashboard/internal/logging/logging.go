// Package logging configures the application's structured logger: log/slog
// emitting JSON records to stdout at a configured level.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"strings"
)

// ParseLevel maps a level name (debug|info|warn|error) to a slog.Level,
// erroring on anything else. Surrounding whitespace and case are ignored.
func ParseLevel(name string) (slog.Level, error) {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "debug":
		return slog.LevelDebug, nil
	case "info":
		return slog.LevelInfo, nil
	case "warn":
		return slog.LevelWarn, nil
	case "error":
		return slog.LevelError, nil
	default:
		return 0, fmt.Errorf("invalid log level %q (want debug|info|warn|error)", name)
	}
}

// New returns a slog.Logger that writes JSON records at or above level to w.
// It does not mutate the package default; callers inject the returned logger
// explicitly (and may slog.SetDefault it themselves if they want package-level
// logging).
func New(level slog.Level, w io.Writer) *slog.Logger {
	h := slog.NewJSONHandler(w, &slog.HandlerOptions{Level: level})
	return slog.New(h)
}
