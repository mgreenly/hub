# ledger

The **ledger** service for the metaspot single-tenant suite. A pure MCP API with
**no UI** and **no token logic**, deployed at `<account>.metaspot.org/srv/ledger/`
(e.g. `ai.metaspot.org/srv/ledger/`). First demo account: **ai**.

**This is a skeleton.** It exists to be the *second* service on the box, proving
the dashboard's multi-app mechanics end to end (service inventory, per-mount nginx
routing, per-resource token binding, per-app identity passthrough). It was
duplicated from `../crm` with the contacts domain stripped out. The only MCP tool
is `ledger_whoami` — the end-to-end auth proof. Real ledger domain logic comes
later; until then, **cut breadth, keep depth**: every line that *is* here is the
production-grade crm chassis, renamed.

**Read the decisions first — do not re-derive them:**

- `../metaspot/AGENTS.md` — platform spec (Service layer = path routing).
- `../metaspot/docs/path-routing-architecture.md` — server-side topology + the
  auth contract you live under.
- `../metaspot/docs/connector-and-install.md` — the suite plugin + install layer.
  Note: a service's connector **skills live in the `dashboard` repo's `plugin/`**,
  not here.
- `../crm` — the sibling service this was duplicated from. The reference for how a
  real domain (`internal/contacts`) wires into this chassis.

If anything here conflicts with those docs, the docs win — and flag the conflict.

## What this app is

A loopback-only domain service. nginx (owned by the dashboard) terminates TLS,
introspects every request via `auth_request` against the dashboard, and injects
`X-Owner-Email` / `X-Client-Id`. This service **trusts those headers** and does
no token validation of its own. nginx strips the `/srv/ledger/` prefix, so
internally routes stay bare (`/mcp`, `/.well-known/...`). Small business, ≤100
users: SQLite, single instance, is correct and deliberate.

## What's in the skeleton

- **`internal/mcp`** — JSON-RPC 2.0 MCP transport. One tool: `ledger_whoami`,
  returning the authenticated `owner_email` + `client_id` from the nginx-injected
  headers. New domain tools are added to `toolDescriptors()` and `dispatchTool()`.
- **`internal/server`** — routing, the unauthenticated RFC 9728
  protected-resource metadata document, the `requireIdentityHeaders` gate, the
  `/whoami` proof handler, security headers, graceful shutdown.
- **`internal/db`** — SQLite open (WAL, FK, single-writer) + embedded migration
  runner. **Boots and migrates, but no tool reads it yet.** This is the wired seam
  where real ledger tables and a domain service attach — the same way `../crm`
  wires `internal/contacts`. Migration `001_schema_migrations` is the only one;
  add `002_*.sql` when the domain arrives.
- **`internal/logging`, `internal/ids`** — structured slog + request-id
  middleware, ULID generation. Carried from the chassis unchanged.

## Adding the real domain later

1. Add `internal/ledger/` (types/service/repo), mirroring `../crm/internal/contacts`.
2. Add `internal/db/migrations/002_ledger.sql`.
3. Inject the domain service into `mcp.NewHandler(...)` and store it on `Handler`.
4. Add tools to `toolDescriptors()` + `dispatchTool()`; keep `ledger_whoami`.

## nginx fragment (not a vhost)

This service's `bin/setup` writes only `/etc/nginx/conf.d/locations/ledger.conf`
(its `location /srv/ledger/` + the PRM well-known location, per
`path-routing-architecture.md`) and reloads nginx. It does **not** install a
server block and does **not** issue a TLS cert — the dashboard owns both. A dev
mirror of this fragment lives at `../nginx/locations/ledger.conf`.

## Manifest / deploy

`etc/manifest.env`: `APP=ledger`, `MOUNT=/srv/ledger/`, `DEFAULT=false`,
`PORT=3002` (loopback), `MCP=true` (so the dashboard inventory lists it). Five
`bin/*` scripts (build/start/stop/setup/deploy). No `plugin/` in this repo.
