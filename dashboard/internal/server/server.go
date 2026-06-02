// Package server builds and runs the dashboard's HTTP server: routing, the
// index page, static assets, security headers, and graceful shutdown.
package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"dashboard/internal/audit"
	"dashboard/internal/googleidp"
	"dashboard/internal/grantevents"
	"dashboard/internal/oauth"
	"dashboard/internal/oauthstate"
	"dashboard/internal/ratelimit"
	"dashboard/internal/session"
	"dashboard/ui"
)

// shutdownTimeout bounds how long Run waits for in-flight requests to finish
// before forcing the server down.
const shutdownTimeout = 10 * time.Second

// Options configures the HTTP server.
type Options struct {
	Addr            string                     // listen address, e.g. "127.0.0.1:3000"
	Logger          *slog.Logger               // structured logger (required)
	IDPProvider     googleidp.Provider         // Google identity-provider seam (required for login)
	PublicBaseURL   string                     // public origin, e.g. "https://ai.metaspot.org" (for the OAuth redirect URI)
	Handshakes      *oauthstate.HandshakeStore // login-handshake store (required for login)
	WorkspaceDomain string                     // Google Workspace hosted domain federation is restricted to (required for login)
	Sessions        *session.SessionStore      // web-session store (required for login)

	// OAuth authorization-server collaborators (required).
	DB           *sql.DB              // shared database handle (token-exchange transactions)
	OAuthClients *oauth.ClientStore   // DCR client registrations
	OAuthCodes   *oauth.AuthCodeStore // short-lived authorization codes
	OAuthTokens  *oauth.TokenStore    // chains, access tokens, refresh tokens
	Audit        *audit.Log           // security-audit log

	// Resources is the configured set of resource identifiers (one or more) the
	// authorization server will mint tokens for. Required.
	Resources []string
	// Admins may introspect any owner's tokens. May be empty.
	Admins []string

	// RateLimiter is the per-token sliding-window limiter the /internal/authn
	// introspection endpoint applies after a token validates. Required.
	RateLimiter *ratelimit.Limiter

	// GrantEvents is the in-process pub/sub that keeps the index page's
	// live-grants block fresh: token issuance/refresh/revocation publish on it,
	// the SSE handler subscribes to it. Required.
	GrantEvents *grantevents.Bus

	// ManifestRoot is the directory under which each service drops its
	// <name>/etc/manifest.env, read by the /services inventory endpoint.
	// Defaults to "/opt" when empty.
	ManifestRoot string
}

// app holds the HTTP layer's dependencies. Handlers are methods on app, so new
// collaborators (sessions, tokens, config) become struct fields rather than
// ever-longer handler parameter lists. It is unexported: the package's public
// surface is New/Run, not the struct.
type app struct {
	logger          *slog.Logger
	tmpl            *template.Template
	static          fs.FS
	idpProvider     googleidp.Provider
	publicBaseURL   string
	handshakes      *oauthstate.HandshakeStore
	workspaceDomain string
	sessions        *session.SessionStore

	db           *sql.DB
	oauthClients *oauth.ClientStore
	oauthCodes   *oauth.AuthCodeStore
	oauthTokens  *oauth.TokenStore
	audit        *audit.Log
	resources    []string
	admins       []string
	rateLimiter  *ratelimit.Limiter
	manifestRoot string
	grantEvents  *grantevents.Bus
}

// New builds the HTTP server with its routes (index + static assets), security
// headers, and pinned timeouts. Templates are parsed here so a broken template
// fails startup loudly rather than at first request. It does not start listening.
func New(opts Options) (*http.Server, error) {
	if opts.Logger == nil {
		return nil, errors.New("server: Logger is required")
	}
	if opts.IDPProvider == nil {
		return nil, errors.New("server: IDPProvider is required")
	}
	if opts.Handshakes == nil {
		return nil, errors.New("server: Handshakes is required")
	}
	if opts.WorkspaceDomain == "" {
		return nil, errors.New("server: WorkspaceDomain is required")
	}
	if opts.Sessions == nil {
		return nil, errors.New("server: Sessions is required")
	}
	if opts.DB == nil {
		return nil, errors.New("server: DB is required")
	}
	if opts.OAuthClients == nil {
		return nil, errors.New("server: OAuthClients is required")
	}
	if opts.OAuthCodes == nil {
		return nil, errors.New("server: OAuthCodes is required")
	}
	if opts.OAuthTokens == nil {
		return nil, errors.New("server: OAuthTokens is required")
	}
	if opts.Audit == nil {
		return nil, errors.New("server: Audit is required")
	}
	if len(opts.Resources) == 0 {
		return nil, errors.New("server: at least one Resource is required")
	}
	if opts.RateLimiter == nil {
		return nil, errors.New("server: RateLimiter is required")
	}
	if opts.GrantEvents == nil {
		return nil, errors.New("server: GrantEvents is required")
	}

	manifestRoot := opts.ManifestRoot
	if manifestRoot == "" {
		manifestRoot = "/opt"
	}

	// Parse the index page together with the partials it embeds (the
	// live-grants block, which the /grants/fragment handler also renders
	// stand-alone). Both files share one template set so {{template "grants_block"}}
	// resolves and a broken partial fails startup loudly.
	tmpl, err := template.ParseFS(ui.Files,
		"html/index.html",
		"html/partials/grants_block.tmpl",
		"html/partials/install_block.tmpl",
		"html/partials/install_card.tmpl",
	)
	if err != nil {
		return nil, fmt.Errorf("parse templates: %w", err)
	}

	static, err := fs.Sub(ui.Files, "static")
	if err != nil {
		return nil, fmt.Errorf("static subtree: %w", err)
	}

	a := &app{
		logger:          opts.Logger,
		tmpl:            tmpl,
		static:          static,
		idpProvider:     opts.IDPProvider,
		publicBaseURL:   opts.PublicBaseURL,
		handshakes:      opts.Handshakes,
		workspaceDomain: opts.WorkspaceDomain,
		sessions:        opts.Sessions,
		db:              opts.DB,
		oauthClients:    opts.OAuthClients,
		oauthCodes:      opts.OAuthCodes,
		oauthTokens:     opts.OAuthTokens,
		audit:           opts.Audit,
		resources:       opts.Resources,
		admins:          opts.Admins,
		rateLimiter:     opts.RateLimiter,
		manifestRoot:    manifestRoot,
		grantEvents:     opts.GrantEvents,
	}

	srv := &http.Server{
		Addr:              opts.Addr,
		Handler:           a.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	return srv, nil
}

// Run starts srv and blocks until ctx is cancelled, then shuts it down
// gracefully within shutdownTimeout. A clean shutdown returns nil; a listen
// failure returns that error.
func Run(ctx context.Context, srv *http.Server, logger *slog.Logger) error {
	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		// Server stopped on its own before any shutdown signal.
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		logger.Info("shutdown initiated")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		logger.Info("shutdown complete")
		return nil
	}
}
