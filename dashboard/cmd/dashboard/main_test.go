package main

import (
	"bytes"
	"strings"
	"testing"
)

// noEnv is a getenv that reports every variable as unset.
func noEnv(string) string { return "" }

// envMap returns a getenv backed by m.
func envMap(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

// runArgs drives run with empty stdin and captured output.
func runArgs(t *testing.T, getenv func(string) string, args ...string) (out, errOut string, err error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err = run(args, getenv, strings.NewReader(""), &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

func TestRunVersion(t *testing.T) {
	out, _, err := runArgs(t, noEnv, "--version")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(out) != version {
		t.Errorf("stdout = %q, want %q", strings.TrimSpace(out), version)
	}
}

func TestRunNoCommand(t *testing.T) {
	_, _, err := runArgs(t, noEnv)
	if err == nil {
		t.Fatal("want error for missing command, got nil")
	}
}

func TestRunUnknownCommand(t *testing.T) {
	_, _, err := runArgs(t, noEnv, "bogus")
	if err == nil || !strings.Contains(err.Error(), "unknown command") {
		t.Fatalf("want unknown-command error, got %v", err)
	}
}

// Flag separation: a flag must be rejected unless it belongs to the flagset
// that should own it.
func TestRunFlagSeparation(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"global rejects serve flag", []string{"--port", "8080", "serve"}},
		{"serve rejects global flag", []string{"serve", "--version"}},
		{"reset rejects serve flag", []string{"reset", "--port", "9"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, _, err := runArgs(t, noEnv, tc.args...); err == nil {
				t.Errorf("want error for %v, got nil", tc.args)
			}
		})
	}
}

// -h is a successful help request: usage to stderr, no error, exit 0.
func TestRunHelpIsNotError(t *testing.T) {
	for _, args := range [][]string{{"-h"}, {"serve", "-h"}, {"reset", "-h"}} {
		if _, _, err := runArgs(t, noEnv, args...); err != nil {
			t.Errorf("run(%v): help should not error, got %v", args, err)
		}
	}
}

// A malformed DASHBOARD_PORT fails loudly; an unset one falls back to the default.
func TestServeBadPortEnv(t *testing.T) {
	_, _, err := runArgs(t, envMap(map[string]string{"DASHBOARD_PORT": "abc"}), "serve")
	if err == nil || !strings.Contains(err.Error(), "DASHBOARD_PORT") {
		t.Fatalf("want DASHBOARD_PORT error, got %v", err)
	}
}

func TestEnvOr(t *testing.T) {
	if got := envOr(noEnv, "X", "def"); got != "def" {
		t.Errorf("unset: got %q, want def", got)
	}
	if got := envOr(envMap(map[string]string{"X": "v"}), "X", "def"); got != "v" {
		t.Errorf("set: got %q, want v", got)
	}
}

func TestEnvOrInt(t *testing.T) {
	if n, err := envOrInt(noEnv, "X", 3000); err != nil || n != 3000 {
		t.Errorf("unset: got (%d, %v), want (3000, nil)", n, err)
	}
	if n, err := envOrInt(envMap(map[string]string{"X": "8080"}), "X", 3000); err != nil || n != 8080 {
		t.Errorf("valid: got (%d, %v), want (8080, nil)", n, err)
	}
	if _, err := envOrInt(envMap(map[string]string{"X": "abc"}), "X", 3000); err == nil {
		t.Error("invalid: want error, got nil")
	}
}
