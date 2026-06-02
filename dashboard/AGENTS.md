# dashboard

The **dashboard** for the metaspot single-tenant suite. It is the privileged,
per-box "apex" app, deployed at `<account>.metaspot.org/` (e.g.
`ai.metaspot.org/`). First demo account: **ai**.

This is a greenfield repo. **Read the decisions first — do not re-derive them:**

- `../metaspot/AGENTS.md` — platform spec (Service layer = path routing).
- `../metaspot/docs/path-routing-architecture.md` — server-side topology + the
  auth contract you implement.
- `../metaspot/docs/connector-and-install.md` — the suite plugin + install layer
  (this repo hosts the plugin; see below).
- `../crm.bak/` — the prior fused crm+dashboard codebase. **Reference only**, do
  not depend on it. It is ~80% dashboard already; port from it.

If anything here conflicts with those docs, the docs win — and flag the conflict.

## Scope — bounded breadth, production depth (read this before every decision)

**This round is a fully-hardened, production-grade demonstration of a chosen set
of capabilities.** It is not a final product, but it is **not** a throwaway and
**not** a happy-path sketch. The discipline is: **cut breadth, never depth.**

Two jobs: (1) prove the architecture works end to end, and (2) serve as the
**reference template** for when we rebuild the next version (which may or may not
become the production target). A sloppy demo fails both — you can't trust a proof
that only works on the happy path, and you can't template from cut corners.

- **Production-grade on everything in scope.** Every capability we choose to
  demonstrate is built to ship: full error handling, the security/auth hardening
  the docs require, input validation, audit, sane failure behavior, tests. Treat
  the in-scope surface as if it were going to production, because the *next* team
  will copy it. Do **not** cut quality to save time — cut scope instead.
- **Bounded breadth — only the selected capabilities.** We demonstrate a
  deliberately chosen set and **do not expand past it.** No feature, endpoint, or
  abstraction that isn't part of the selected demonstration, however tempting.
  Breadth is fixed; depth is full.
- **Three services or fewer, ever.** This box hosts ≤3 services for the life of
  the demo. Do **not** design, abstract, or hedge for "many services." A literal
  list of 3, hardened, beats a generic registry. If a design only pays off past 3
  services, it is out of scope here — that's breadth we don't buy.
- **No "what it looks like at product scale."** Skip speculative extensibility
  entirely. The next version is a fresh build informed by this template, not an
  evolution of it. Generality for futures this demo won't exercise is waste —
  but that is about *breadth*, never an excuse to under-harden what is in scope.
- **Keep the apex `/` a single hybrid page.** Logged-out = landing/install;
  logged-in = grants/revocation layered on (what crm.bak's `index()` already
  does). Do **not** split it into a separate IAM console — that's product-scale
  breadth this scope rules out. The single page itself is still built to ship.

Default posture: **narrow what you build; harden everything you build.** When in
doubt about *scope*, do less. When in doubt about *quality on something in scope*,
do it properly.

## Build phases

We build in phases — see `docs/phases.md`. Each phase is bounded-breadth /
production-depth and is **not done until it works both on localhost and deployed
on its real DNS name (`ai.metaspot.org`) with real TLS.**

- **Phase 0 (current): structural web app, no auth.** A plain Go web app with all
  the bits in the right structure — serves the index page + static assets, does
  structured logging. Chassis (config, SQLite+migrations, logging, server, CLI,
  banner) + the full deploy spine (manifest/deploy env, seven `bin/*`, systemd
  via the platform launcher, the apex nginx `server` block + HTTP-01 TLS). **No
  auth, no identity, no tokens.** Phase 0 is **fully deployed and serving the
  index over real TLS on `ai.metaspot.org` before Phase 1 begins** — the deploy
  architecture is proven before any auth complexity confounds it.
- **Phase 1: login, identity-aware index, logout.** Layers Google Workspace
  federation + web sessions onto the deployed Phase 0 app; the index becomes
  identity-aware (shows the owner, offers sign-out).
- **Phase 2 and later: MCP and the token leg** — opaque tokens, OAuth AS,
  `/internal/authn`, then plugin/inventory/push.

Full per-phase scope, definition of done, and open decisions are in
`docs/phases.md`.

## What this app is

The apex/`DEFAULT=true` app and the suite's **OAuth authorization server**. An
external IdP (Google) authenticates the human; this app mints its **own opaque
tokens** for use against the services. Services carry no token logic — they trust
identity headers nginx injects after calling this app. Small business, ≤100 users
per box: SQLite, one box, in-process everything is correct and deliberate.

## What it owns

Port from `../crm.bak` (these are already dashboard-shaped there):
`googleidp`, `oauth` (token chains/PKCE/refresh-reuse), `oauthstate`, `session`,
`agentsevents`, `ratelimit`, the `ui/` (login + grants/revocation), the OAuth AS
endpoints. Audit is **per-service** here = auth/token/grant events only.

Build new:
- **`POST /internal/authn`** — loopback-only introspection endpoint nginx calls
  via `auth_request` on every service request. It is the `requireBearer` logic
  from `../crm.bak/internal/server/contacts.go` lifted out of the request path:
  validate the opaque token, check resource binding + workspace + per-token rate
  limit, return `200` + identity headers (`X-Owner-Email`, `X-Client-Id`) or
  `401` (with the MCP `WWW-Authenticate` challenge) / `429`.
- **Push** — VAPID keypair, subscription store, internal send API. Every push
  carries `source` (service name) + `category` for per-source mute. Services are
  publishers; they never own VAPID or subscriptions.
- **Public landing page** — self-templating from the request host; serves the
  one-paste install snippet (no secrets in it).
- **Service inventory** — an endpoint listing the box's services (name, mount,
  MCP resource URL) so the suite plugin's connect skill can wire up each MCP.
- **`plugin/`** — the **one suite plugin per box** lives here (skills for every
  service on the box + the `connect`/doctor skill). This repo is its
  marketplace (`.claude-plugin/marketplace.json`, `source: "./plugin"`). Internal
  only — git-repo source during dev, dashboard-served in prod. NOT the public
  Claude catalog. See `connector-and-install.md` for the skill set (incl. the CRM
  skills, which live here, not in the crm repo).

## What it owns on the box (nginx + TLS)

This app's `bin/setup` owns the **single apex `server` block**, the **one** apex
TLS cert (HTTP-01 `--webroot`) + renewal, the ACME-challenge location, the
`/_authn` internal location, and `include /etc/nginx/conf.d/locations/*.conf;`.
Services only drop `location` fragments into that dir.

## Manifest / deploy

`etc/manifest.env`: `APP=dashboard`, `MOUNT=/`, `DEFAULT=true`, `PORT=3000`
(loopback). Six/seven `bin/*` scripts per `AGENTS.md`. Drop everything
`contacts`/`mcp-crm` from the port — that is the crm service's.
