package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
)

func TestNewEmitsJSONAtLevel(t *testing.T) {
	var buf bytes.Buffer
	log := New(slog.LevelInfo, &buf)

	log.Info("hello", "k", "v")

	var rec map[string]any
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("output is not JSON: %v\n%s", err, buf.String())
	}
	if rec["msg"] != "hello" {
		t.Errorf("msg = %v, want hello", rec["msg"])
	}
	if rec["level"] != "INFO" {
		t.Errorf("level = %v, want INFO", rec["level"])
	}
	if rec["k"] != "v" {
		t.Errorf("attr k = %v, want v", rec["k"])
	}
}

func TestNewFiltersBelowLevel(t *testing.T) {
	var buf bytes.Buffer
	log := New(slog.LevelWarn, &buf)

	log.Info("suppressed")
	if buf.Len() != 0 {
		t.Errorf("Info record below Warn level was emitted: %s", buf.String())
	}

	log.Warn("kept")
	if buf.Len() == 0 {
		t.Error("Warn record at level was not emitted")
	}
}
