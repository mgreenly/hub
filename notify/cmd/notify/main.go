// Command notify is the loopback-only domain service behind nginx. It trusts the
// X-Owner-Email / X-Client-Id headers nginx injects after a successful
// auth_request against the dashboard's authorization server, and performs no
// token logic of its own. See internal/server for the auth contract.
//
// notify is the suite's first event-plane CONSUMER. Alongside its (north/south)
// MCP server it runs a background consumer loop (eventplane/consumer) that
// subscribes to crm's east/west /feed, and for every contact.created event fires
// a best-effort ntfy.sh push (internal/push). The HTTP server and the consumer
// loop share one context and one SQLite database; a STRUCTURAL consumer fault
// crashes the whole process (no half-alive state — event-protocol.md decision
// 11), while a transport fault (crm down) is retried indefinitely inside the
// engine and never brings the service down.
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
	"syscall"

	"eventplane/consumer"

	"notify/internal/db"
	"notify/internal/logging"
	"notify/internal/mcp"
	"notify/internal/push"
	"notify/internal/server"
)

// version is the product version, overridden at build time via -ldflags.
var version = "dev"

// The event-plane upstream notify consumes and the stable id it presents on every
// connect (event-protocol.md §7.1; decision 10). Both are fixed constants — notify
// consumes exactly crm's feed, and its X-Consumer-Id is the literal "notify".
const (
	upstreamSource = "crm"
	consumerID     = "notify"
)

func main() {
	if err := run(os.Args[1:], os.Getenv, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "notify:", err)
		os.Exit(1)
	}
}

// config is the fully-resolved deployment configuration, read once at this
// composition root (§3) so nothing deeper touches the environment.
type config struct {
	ip         string
	port       int
	logLevel   string
	resourceID string
	authServer string
	dbPath     string

	// Event-plane consumer config.
	feedURL string // crm's loopback feed (NOTIFY_FEED_URL)
	from    string // first-subscription choice: tail|earliest (NOTIFY_FROM)

	// ntfy push config. ntfyBase is plain config (so tests/dev can point at a
	// mock); ntfyTopic and ntfyToken are deployment SECRETS injected via the
	// environment (.envrc locally; app-config in prod), never read from source.
	ntfyBase  string
	ntfyTopic string
	ntfyToken string
}

func run(args []string, getenv func(string) string, stdout, stderr io.Writer) error {
	portDef, err := envOrInt(getenv, "NOTIFY_PORT", 3003)
	if err != nil {
		return err
	}

	fs := flag.NewFlagSet("notify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	showVersion := fs.Bool("version", false, "print version and exit")
	// Bind 127.0.0.1 by default and in production: nginx is the only ingress
	// and sets identity headers authoritatively. Binding a public interface
	// would let anyone connect directly and spoof X-Owner-Email — a security
	// defect. The flag exists only so tests/local runs can override deliberately.
	ip := fs.String("ip", envOr(getenv, "NOTIFY_IP", "127.0.0.1"), "listen address — keep loopback (env: NOTIFY_IP)")
	port := fs.Int("port", portDef, "listen port (env: NOTIFY_PORT)")
	logLevel := fs.String("log-level", envOr(getenv, "NOTIFY_LOG_LEVEL", "info"), "log level: debug|info|warn|error (env: NOTIFY_LOG_LEVEL)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *showVersion {
		fmt.Fprintln(stdout, version)
		return nil
	}

	// NOTIFY_RESOURCE_ID is this service's canonical resource identifier (must be
	// byte-equal to the `resource` in the PRM doc and the dashboard's token
	// binding). NOTIFY_AUTH_SERVER is the dashboard authorization-server base URL
	// advertised to clients. Both have local-dev defaults; we resolve them here
	// at the boundary so nothing deeper reads the environment.
	cfg := config{
		ip:         *ip,
		port:       *port,
		logLevel:   *logLevel,
		resourceID: envOr(getenv, "NOTIFY_RESOURCE_ID", "http://localhost:8080/srv/notify/mcp"),
		authServer: envOr(getenv, "NOTIFY_AUTH_SERVER", "http://localhost:8080"),
		// NOTIFY_DB_PATH is the SQLite database file. db.Open pins
		// SetMaxOpenConns(1) for single-writer discipline (the consumer is the only
		// writer of feed_offset); we resolve the path here at the boundary.
		dbPath: envOr(getenv, "NOTIFY_DB_PATH", "./tmp/notify.db"),
		// NOTIFY_FEED_URL is crm's loopback feed (§3). The event plane bypasses
		// nginx (§2), so this is a direct 127.0.0.1 address.
		feedURL: envOr(getenv, "NOTIFY_FEED_URL", "http://127.0.0.1:3001/feed"),
		// NOTIFY_FROM is the first-subscription choice (§7.1); tail by default so a
		// fresh notify only pushes for contacts created from now on.
		from:      envOr(getenv, "NOTIFY_FROM", "tail"),
		ntfyBase:  envOr(getenv, "NOTIFY_NTFY_BASE_URL", "https://ntfy.sh"),
		ntfyTopic: getenv("NTFY_TOPIC"),
		ntfyToken: getenv("NTFY_API_KEY"),
	}

	// Fail loudly at the boundary if a required secret is absent: the push hop
	// cannot work without its topic and key, and silently degrading would hide a
	// misconfigured deploy. Inject them via .envrc from ~/.secrets/ (local) or
	// app-config (prod) — never in source.
	if cfg.ntfyTopic == "" {
		return errors.New("NTFY_TOPIC is required (inject via .envrc from ~/.secrets/NTFY_TOPIC)")
	}
	if cfg.ntfyToken == "" {
		return errors.New("NTFY_API_KEY is required (inject via .envrc from ~/.secrets/NTFY_API_KEY)")
	}

	return serve(cfg, stdout)
}

// serve runs the HTTP server and the event-plane consumer loop together until
// interrupted or until either fails. They share one context: a SIGTERM cancels
// both; a structural consumer fault cancels the server too, so the process exits
// non-zero rather than lingering HTTP-up / consumer-dead (decision 11).
func serve(cfg config, stdout io.Writer) error {
	level, err := logging.ParseLevel(cfg.logLevel)
	if err != nil {
		return err
	}
	logger := logging.New(level, stdout)

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// A cancelable child so either subsystem can tear down the other on a fatal
	// error, independent of the signal.
	ctx, cancel := context.WithCancel(sigCtx)
	defer cancel()

	// One SQLite database, shared by the (currently unused) MCP seam and the
	// consumer's feed_offset store. The consumer is the only writer of feed_offset.
	conn, err := db.Open(cfg.dbPath)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := db.Migrate(ctx, conn); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}

	mcpHandler := mcp.NewHandler()

	addr := net.JoinHostPort(cfg.ip, strconv.Itoa(cfg.port))
	srv, err := server.New(server.Options{
		Addr:       addr,
		Logger:     logger,
		ResourceID: cfg.resourceID,
		AuthServer: cfg.authServer,
		MCP:        mcpHandler,
	})
	if err != nil {
		return err
	}

	// The event-plane consumer: subscribe to crm's feed, push best-effort on
	// contact.created.
	pushClient := push.NewClient(cfg.ntfyBase, cfg.ntfyTopic, cfg.ntfyToken, logger)
	consumerCfg := consumer.Config{
		FeedURL:    cfg.feedURL,
		From:       cfg.from,
		DB:         conn,
		Source:     upstreamSource,
		ConsumerID: consumerID,
		Logger:     logger,
	}

	// NOTE: ntfy topic/token are deliberately omitted from this log line — they
	// are secrets (the secrets skill's hard rule).
	logger.Info("starting notify",
		"addr", addr, "resource_id", cfg.resourceID, "auth_server", cfg.authServer,
		"db_path", cfg.dbPath, "feed_url", cfg.feedURL, "from", cfg.from,
		"ntfy_base", cfg.ntfyBase, "version", version)

	// Run the server and the consumer concurrently. errCh collects both
	// terminations; the first fatal error cancels ctx so the other unwinds.
	errCh := make(chan error, 2)
	go func() {
		err := consumer.Run(ctx, consumerCfg, push.Handler(pushClient, logger))
		if err != nil {
			err = fmt.Errorf("event-plane consumer: %w", err)
		}
		cancel() // bring the server down too — no half-alive state (decision 11)
		errCh <- err
	}()
	go func() {
		errCh <- server.Run(ctx, srv, logger)
	}()

	var firstErr error
	for i := 0; i < 2; i++ {
		if e := <-errCh; e != nil && firstErr == nil {
			firstErr = e
		}
	}
	return firstErr
}

func envOr(getenv func(string) string, key, def string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return def
}

// envOrInt returns def when key is unset/empty, the parsed value when it holds
// a valid integer, and an error naming the variable otherwise — a malformed
// override fails loudly rather than silently reverting to def.
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
