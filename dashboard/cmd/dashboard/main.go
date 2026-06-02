package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"dashboard/internal/audit"
	"dashboard/internal/db"
	"dashboard/internal/googleidp"
	"dashboard/internal/grantevents"
	"dashboard/internal/logging"
	"dashboard/internal/oauth"
	"dashboard/internal/oauthstate"
	"dashboard/internal/ratelimit"
	"dashboard/internal/server"
	"dashboard/internal/session"
)

// version is the product version, overridden at build time via -ldflags.
// It will move to internal/version once that package exists.
var version = "dev"

func main() {
	if err := run(os.Args[1:], os.Getenv, os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "dashboard:", err)
		os.Exit(1)
	}
}

func run(args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) error {
	// Global flagset: only flags valid before the command. Parsing stops at the
	// first non-flag argument, which is the command. Per-command flags are
	// parsed by each subcommand's own flagset from the args after the command.
	global := flag.NewFlagSet("dashboard", flag.ContinueOnError)
	global.SetOutput(stderr)
	showVersion := global.Bool("version", false, "print version and exit")
	global.Usage = func() {
		fmt.Fprintf(stderr, "Usage: dashboard [--version] <command> [command flags]\n")
		fmt.Fprintf(stderr, "Commands:\n  serve   run the HTTP server\n  reset   wipe the local SQLite database\n")
	}
	if err := global.Parse(args); err != nil {
		// -h/-help is a successful help request, not an error.
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *showVersion {
		fmt.Fprintln(stdout, version)
		return nil
	}

	cmd := global.Arg(0)
	cmdArgs := global.Args()
	if len(cmdArgs) > 0 {
		cmdArgs = cmdArgs[1:]
	}

	switch cmd {
	case "serve":
		return cmdServe(cmdArgs, getenv, stdout, stderr)
	case "reset":
		return cmdReset(cmdArgs, getenv, stdin, stdout, stderr)
	case "":
		global.Usage()
		return fmt.Errorf("no command given")
	default:
		global.Usage()
		return fmt.Errorf("unknown command: %s", cmd)
	}
}

// cmdServe parses the serve subcommand's flags and runs the server.
func cmdServe(args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	portDef, err := envOrInt(getenv, "DASHBOARD_PORT", 3000)
	if err != nil {
		return err
	}
	// Per-token introspection rate limit applied by POST /internal/authn.
	authnRateLimit, err := envOrInt(getenv, "DASHBOARD_AUTHN_RATE_LIMIT", 60)
	if err != nil {
		return err
	}
	authnRateWindow, err := envOrDuration(getenv, "DASHBOARD_AUTHN_RATE_WINDOW", 10*time.Second)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", envOr(getenv, "DASHBOARD_DB", "./dashboard.db"), "SQLite database path (env: DASHBOARD_DB)")
	ip := fs.String("ip", envOr(getenv, "DASHBOARD_IP", "127.0.0.1"), "listen address (env: DASHBOARD_IP)")
	port := fs.Int("port", portDef, "listen port (env: DASHBOARD_PORT)")
	logLevel := fs.String("log-level", envOr(getenv, "DASHBOARD_LOG_LEVEL", "info"), "log level: debug|info|warn|error (env: DASHBOARD_LOG_LEVEL)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	// Google credentials are env-only, never flags: a client secret on a --flag
	// would be visible in ps output and shell history. Required at the boundary
	// so a missing secret fails loudly here rather than as a downstream Google
	// 400. CLIENT_SECRET isn't consumed until the code exchange, but login can't
	// work without it, so its presence is required now too.
	if err := requireEnv(getenv, "GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET", "GOOGLE_WORKSPACE_DOMAIN", "DASHBOARD_RESOURCES"); err != nil {
		return err
	}
	creds := googleidp.Credentials{
		ClientID:        getenv("GOOGLE_CLIENT_ID"),
		ClientSecret:    getenv("GOOGLE_CLIENT_SECRET"),
		WorkspaceDomain: getenv("GOOGLE_WORKSPACE_DOMAIN"),
	}
	// DASHBOARD_RESOURCES is the comma-separated set of resource identifiers (1..3)
	// the authorization server mints tokens for; DASHBOARD_ADMINS is an optional
	// comma-separated set of owner emails permitted to introspect any chain.
	resources := splitList(getenv("DASHBOARD_RESOURCES"))
	admins := splitList(getenv("DASHBOARD_ADMINS"))
	// publicBaseURL is the exact origin Google redirects back to and that the
	// later code-exchange must resend verbatim; it must match the redirect URI
	// registered in the Google Cloud console.
	publicBaseURL := envOr(getenv, "DASHBOARD_PUBLIC_BASE_URL", "http://localhost:3000")
	return serve(*dbPath, *ip, *port, *logLevel, creds, publicBaseURL, resources, admins, authnRateLimit, authnRateWindow, stdout, stderr)
}

// cmdReset parses the reset subcommand's flags and wipes the database.
func cmdReset(args []string, getenv func(string) string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("reset", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dbPath := fs.String("db", envOr(getenv, "DASHBOARD_DB", "./dashboard.db"), "SQLite database path (env: DASHBOARD_DB)")
	yes := fs.Bool("yes", false, "skip confirmation prompt")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	return reset(*dbPath, *yes, stdin, stdout, stderr)
}

// serve runs the long-running HTTP server until interrupted. It opens the
// database, builds the login + OAuth authorization-server collaborators over
// that one handle, and hands them to the server.
func serve(dbPath, ip string, port int, logLevel string, creds googleidp.Credentials, publicBaseURL string, resources, admins []string, authnRateLimit int, authnRateWindow time.Duration, stdout, stderr io.Writer) error {
	level, err := logging.ParseLevel(logLevel)
	if err != nil {
		return err
	}
	logger := logging.New(level, stdout)

	addr := net.JoinHostPort(ip, strconv.Itoa(port))
	idpProvider := googleidp.New(creds)
	database, err := db.Open(dbPath)
	if err != nil {
		return err
	}
	handshakes := oauthstate.NewHandshakeStore(database, 5*time.Minute)
	sessions := session.NewSessionStore(database)
	// Token lifetimes follow the prior crm.bak deployment: short-lived access
	// tokens, long-lived rotating refresh tokens, briefly-valid authorization codes.
	oauthClients := oauth.NewClientStore(database)
	oauthCodes := oauth.NewAuthCodeStore(database, 2*time.Minute)
	oauthTokens := oauth.NewTokenStore(database, 30*time.Minute, 30*24*time.Hour)
	auditLog := audit.New(database)
	srv, err := server.New(server.Options{
		Addr:            addr,
		Logger:          logger,
		IDPProvider:     idpProvider,
		PublicBaseURL:   publicBaseURL,
		Handshakes:      handshakes,
		WorkspaceDomain: creds.WorkspaceDomain,
		Sessions:        sessions,
		DB:              database,
		OAuthClients:    oauthClients,
		OAuthCodes:      oauthCodes,
		OAuthTokens:     oauthTokens,
		Audit:           auditLog,
		Resources:       resources,
		Admins:          admins,
		RateLimiter:     ratelimit.New(authnRateLimit, authnRateWindow),
		GrantEvents:     grantevents.New(),
	})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("starting dashboard", "addr", addr, "version", version)
	return server.Run(ctx, srv, logger)
}

// reset wipes the local SQLite database, prompting unless yes is set. Stubbed.
func reset(dbPath string, yes bool, stdin io.Reader, stdout, stderr io.Writer) error {
	panic("reset: not implemented")
}

// requireEnv returns an error naming every listed variable that is unset or
// empty, reporting them all at once so a misconfigured boot surfaces its full
// list of missing secrets in a single message. It checks presence only — it
// never reads or echoes a value.
func requireEnv(getenv func(string) string, names ...string) error {
	var missing []string
	for _, name := range names {
		if getenv(name) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}
	return nil
}

// splitList parses a comma-separated environment value into a slice, trimming
// surrounding whitespace from each element and dropping empties. An empty or
// all-separator input yields a nil slice.
func splitList(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func envOr(getenv func(string) string, key, def string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return def
}

// envOrInt returns def when key is unset/empty, the parsed value when it holds
// a valid integer, and an error naming the variable when it holds anything else
// — a malformed override fails loudly rather than silently reverting to def.
func envOrInt(getenv func(string) string, key string, def int) (int, error) {
	v := getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid integer %q", key, v)
	}
	return n, nil
}

// envOrDuration returns def when key is unset/empty, the parsed value when it
// holds a valid Go duration (e.g. "10s", "1m"), and an error naming the
// variable when it holds anything else.
func envOrDuration(getenv func(string) string, key string, def time.Duration) (time.Duration, error) {
	v := getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: invalid duration %q", key, v)
	}
	return d, nil
}
