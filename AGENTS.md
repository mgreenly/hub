# hub

The deployable **application suite** for metaspot — the dashboard plus its
services that run *on* a metaspot box. This folder is just a parent holding five
**independent git repos** (each has its own `.git`); `hub` itself is not a repo.
Four are the on-box suite (below); the fifth, `marketplace`, is a GitHub-hosted
plugin marketplace for delivering skills/commands (and Claude Code MCP config)
to Claude clients — not an on-box service.

The **infrastructure** half of the system lives in the sibling repo
`../metaspot` (Terraform: AWS org/accounts, DNS, the one-box-per-customer
platform). metaspot builds the box; hub is what gets installed on it.

## Authoritative specs live in metaspot, not here

Do not re-derive the architecture — read these first; on conflict, they win:

- `../metaspot/AGENTS.md` — platform spec (servers, launcher, SSM `app-config`
  secrets, per-account backup buckets, the Service layer).
- `../metaspot/docs/path-routing-architecture.md` — server-side topology + the
  nginx `auth_request` → dashboard introspection **auth contract** every service
  lives under.
- `../metaspot/docs/connector-and-install.md` — the suite plugin + client-side
  install/connector layer.

## The model in one paragraph

One box per customer answers on the apex `<account>.metaspot.org`. The suite is
one **dashboard** (apex/`DEFAULT` app — OAuth authorization server, IAM, push,
install landing) plus N **services** (pure REST + MCP, **no UI**, **no token
logic**), all bound to loopback, routed by **path** under the reserved `/srv/`
prefix. nginx is the sole trust boundary: it introspects every `/srv/<svc>/`
request against the dashboard's `/internal/authn` and injects `X-Owner-Email` /
`X-Client-Id`, which services trust blindly. The `/srv/<svc>/` prefix is stripped
before proxying, so a service's internal routes stay `/contacts`, `/mcp`, etc.

## The projects

| dir | role | runtime | loopback port | git |
|---|---|---|---|---|
| **dashboard** | apex/`DEFAULT` app: OAuth AS, IAM, push, install landing, service inventory | Go (`module dashboard`), SQLite (`dashboard.db`) | **3000** | own repo |
| **crm** | path-routed service `/srv/crm/` — contacts domain + MCP | Go (`module crm`), SQLite | **3001** | own repo |
| **ledger** | path-routed service `/srv/ledger/` — newest, skeleton (whoami-only, cloned from crm) | Go (`module ledger`) | **3002** | own repo |
| **nginx** | local-dev front door (:8080) mirroring the prod `/srv/<svc>/` routing | nginx conf + `run` script | — | own repo |
| **marketplace** | plugin marketplace — delivers skills/commands (and Claude Code MCP config) to Claude clients; **not** an on-box service | GitHub-hosted | — | own repo (`mgreenly/marketplace`, public) |

Each service exposes a no-side-effect `<svc>_whoami` MCP tool (proves the
plugin → connector → dashboard OAuth → service chain end to end). Services keep
their own per-service audit store; the dashboard audits auth/token/grant events.

## Per-service conventions (from the metaspot Service layer)

- Install root `/opt/<app>/`, dedicated `--system` user, single entrypoint
  `/opt/<app>/bin/run`.
- `bin/` script interface: services ship `build deploy setup start stop`; the
  dashboard adds `backup restore secrets teardown` (and its named binary).
- `etc/manifest.env` — flat `KEY=value`, carries `MOUNT=/srv/<svc>/` + `PORT`;
  dashboard is `DEFAULT=true`.
- `etc/nginx.conf` — the service's `/srv/<svc>/` location fragment. The dashboard
  owns the apex `server{}` block, the cert, and the `/_authn` hook; a service's
  `bin/setup` only drops its fragment into `/etc/nginx/conf.d/locations/<svc>.conf`.
- Secrets via SSM `/metaspot/<env>/app-config` (never in Terraform/source).

## Local dev

`nginx/run` is the dev front door on **:8080** — it computes its own dir, proxies
the apex to dashboard (:3000), and includes `nginx/locations/*.conf` (the dev
mirror of each prod `/srv/<svc>/` fragment, currently crm + ledger). Run each Go
service on its loopback port, then `cd nginx && ./run`.

## Deployments (how to ship to the box)

Production is the box at `<account>.metaspot.org` (first/only account: **ai**).
**There are no git remotes — deploy is rsync of built artifacts, not `git push`.**
Commits are local version-control hygiene; shipping is the `bin/*` scripts below.
Work the repos in dependency order and verify on the box after each step.

**The `bin/*` lifecycle, setup → teardown** (services ship a subset; the
dashboard ships them all — see Per-service conventions): `setup` (one-time
provision) · `secrets` (seed/rotate the SSM app-config key) · `build` (off-box
artifacts) · `deploy` (ship + restart) · `start` / `stop` (systemd control) ·
`backup` / `restore` (snapshot the SQLite state to the per-account bucket) ·
`teardown` (remove the app from the box — reverse of `setup`). The detail below
covers the deploy-critical ones.

**Routing/auth config per service lives in `etc/deploy.env`** (non-secret):
`ACCOUNT`, `SSH_USER` (`ec2-user`), `SSH_KEY` (`~/.ssh/id_ed25519_ai4mgreenly`);
`HOST` defaults to `${ACCOUNT}.metaspot.org`. AWS = `--profile ${ACCOUNT} --region
us-east-2`; SSM/secrets steps need a live SSO session (`aws sso login --profile
ai` — interactive, the user runs it; the token expires).

**`bin/build`** — off-box, deterministic. `CGO_ENABLED=0 GOOS=linux GOARCH=amd64`
→ `build/<app>.bin` (the Go binary) **and** `build/<app>` (a shell wrapper that
becomes `/opt/<app>/bin/run`; sets non-secret public config from `METASPOT_DOMAIN`
and execs the binary on `127.0.0.1:$PORT`). Eventplane consumers carry a committed
`replace eventplane => ../eventplane`, so the build needs the sibling source tree
but no network/go.work.

**`bin/deploy`** — `build` → `ssh systemctl stop` → rsync `build/<app>`→
`/opt/<app>/bin/run`, `build/<app>.bin`→`/opt/<app>/bin/<app>.bin`,
`etc/manifest.env`→`/opt/<app>/etc/`, → `chown` + `systemctl start` +
`is-active`. **Assumes `bin/setup` already ran** and the dashboard's apex setup
(server block, the one TLS cert, `/_authn`, `conf.d/locations/` dir) exists.
Never touches `/opt/<app>/data/<app>.db` — SQLite state is created on first start;
**migrations run on start**.

**`bin/setup`** — first-time, idempotent box prep for a service: creates the
`--system` app user + `/opt/<app>` tree, writes & **enables (not starts)** the
systemd unit (`ExecStart=/usr/local/bin/metaspot-launch <app>`), drops the nginx
fragment to `/etc/nginx/conf.d/locations/<app>.conf`, `nginx -t`, reload. Owns
nothing global. Required once before a brand-new service's first `bin/deploy`.

**Secrets** — single SSM SecureString `/metaspot/<account>/app-config`: a JSON
blob keyed per app. `metaspot-launch` extracts `.["<app>"]` at every start,
exports each `KEY=value`, and **hard-fails if the key/param is missing** — so
secrets must be seeded *before* first start. `bin/secrets` (dashboard, notify)
does a **non-destructive read-modify-write of only its own key**, values pulled
from `~/.secrets/<NAME>` (never read/printed; masked in the summary), siblings
preserved. The launcher injects them into the process env only — never on disk.

**Registering a new MCP service with the dashboard (easy to miss).** The
dashboard AS resource list is hardcoded in **`dashboard/bin/build` →
`DASHBOARD_RESOURCES`** (comma-separated `https://${METASPOT_DOMAIN}/srv/<svc>/mcp`).
A new service **must** be added there, or `/internal/authn` returns *"unknown
service for original request URI"* with **no `resource_metadata`** in the 401
challenge → the MCP client can't discover the PRM → omits the `resource`
indicator → the AS rejects authorize with `invalid_target: resource is required`.
Fixing it means editing `bin/build` and **redeploying the dashboard** (a dashboard
restart briefly drops `/internal/authn` for the *whole box* — seconds).

**Order to bring up a new consumer service (notify was the worked example):**
1. seed secrets → `bin/secrets` (after SSO login),
2. deploy any **producer it consumes first** (e.g. crm's `/feed`) so it's live,
3. `bin/setup` (provision), 4. `bin/deploy` (ship + start),
5. add to `DASHBOARD_RESOURCES` + redeploy dashboard,
6. verify.

**Verify on the box:** `systemctl is-active <svc>`; `journalctl -u <svc>` for the
boot/migration/consumer lines (note: best-effort paths like notify's push log
only at **Debug** on success — silence ≠ failure; look for `Warn`); loopback
`curl 127.0.0.1:<port>/...` (services trust injected `X-Owner-Email`/`X-Client-Id`
headers, so you can drive `/mcp` directly); PRM `…/.well-known/oauth-protected-
resource` → 200; the `/srv/<svc>/mcp` 401 challenge must carry `resource_metadata`;
then an end-to-end check (event plane: create a row → observe the downstream
effect; auth: connector OAuth round-trip + `<svc>_whoami`).

## MCP client delivery — forced outcomes (2026-06)

How the suite's MCP servers reach Claude clients, and the constraints that
forced the split. The vast majority of users are **Claude Chat / Cowork**;
**Claude Code** users are an extreme minority.

- **Chat / Cowork (the majority): one custom connector per service, added by
  hand.** OAuth-protected remote MCP **cannot** be delivered via a plugin /
  marketplace here — plugin OAuth uses a *localhost* callback, but the Cowork
  agent runs in Anthropic's cloud, so nothing on the user's machine catches the
  redirect. The working path is *Customize → Connectors → "+" → paste the
  service's `/srv/<svc>/mcp` URL → authorize*, once per service. Connector OAuth
  uses Anthropic's **hosted** callback `https://claude.ai/api/mcp/auth_callback`
  — the dashboard OAuth AS must allow-list it and expose discovery
  (`/.well-known/oauth-authorization-server` / `-protected-resource`) + PKCE/DCR.
  At **Team/Enterprise** an admin adds each connector once for the whole org.
  No bundling exists; a single aggregating endpoint was rejected (too many tools).
- **Claude Code (the minority): plugin marketplace and/or the install script.**
  The localhost callback works here, so the plugin marketplace
  (`mgreenly/marketplace` — public, a fifth repo under `hub/`, plugin
  `metaspot-suite@metaspot`) and the `curl … | sh` script (which runs
  `claude mcp add --transport http` per service) both work.

The `marketplace` repo is **kept regardless** — beyond CLI MCP config it's the
general channel for delivering **skills and slash commands** to Claude clients,
which is useful independent of the connector story above.

**Support = user-facing instructions for both paths** (per-connector steps for
Chat/Cowork; script/marketplace for Claude Code) — not automation; the
per-service connector add is irreducible for Cowork.

## Gotcha from the 2026-06 move

These projects used to live directly under `~/projects/`; they were moved into
`~/projects/hub/`. The sub-projects' own docs still reference the **old**
sibling-relative path `../metaspot` (e.g. `crm/CLAUDE.md`), which from
`hub/<svc>/` now resolves to the non-existent `hub/metaspot` — the correct
relative path is `../../metaspot` from a service, or `../metaspot` from this hub
root. A few comments still name `~/projects/nginx` (now `~/projects/hub/nginx`).
All cosmetic — code (Go module names, nginx's self-locating `run`, relative
includes) is unaffected.
