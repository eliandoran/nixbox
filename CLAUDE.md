# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

nixbox is a self-hosted web UI (Go + HTMX, single static binary) for managing a
NixOS server's declarative containers. Users edit each container's config as a
raw Nix expression in the browser; nixbox composes them into the system flake,
runs `nixos-rebuild`, and streams status/logs. It runs as root on the machine
it manages. Milestones 1–2, container logs, and auth (milestone 4: PAM login,
sessions, CSRF) are done; remaining roadmap: generations/rollback, flake input
updates, export (see git log).

## Commands

```bash
# Run Go commands from the devShell: the PAM auth backend is cgo and needs
# libpam from it. CGO_ENABLED=0 works anywhere but swaps in the fail-closed
# PAM stub (and skips the real-PAM tests).
go build ./... && go vet ./... && go test ./...   # standard check
go test ./... -cover                              # per-package coverage (check after every change)
go test ./internal/nix -run TestWriteIndex        # single test
nix build .#nixbox                                # package (update vendorHash in nix/package.nix when go.mod changes)

# App JS — TypeScript in web/src, bundled by esbuild to web/static/app.js
# (a gitignored build product, NOT committed). Bundle once after a fresh
# checkout or any web/src edit — go build/vet/test fail on the missing
# go:embed until then. `just dev` bundles + watches automatically, and
# the nix package bundles in preBuild, so neither needs this by hand:
just bundle       # = esbuild web/src/main.ts --bundle --format=iife --outfile=web/static/app.js
just typecheck    # tsc -p web — esbuild only strips types, it never checks them

# CodeMirror Nix editor bundle — vendored build product, committed at
# web/static/codemirror.js and embedded via //go:embed. Regenerate only
# when the editor source or its deps change (needs nodejs + esbuild from
# the devShell); npm is NOT part of go/nix build. Then commit the result.
(cd web/editor && npm ci && npm run build)

# Dev server — safe on any machine, full UI works (`just dev` wraps this):
NIXBOX_DRY_RUN=1 NIXBOX_TERMINAL=1 NIXBOX_AUTH=none NIXBOX_STATE_DIR=./dev-state go run ./cmd/nixbox serve
# → http://127.0.0.1:8368 (NIXBOX_LISTEN=127.0.0.1:PORT to change)
# NIXBOX_AUTH=none skips the login screen; the binary default is pam, which
# on a machine without an /etc/pam.d/nixbox service rejects every login.

# Dev VM — for REAL rebuilds; never test real applies on the host:
nix build .#vm && ./result/bin/run-testhost-vm
# → UI http://localhost:18368, nginx template container http://localhost:18080
# Web login (real PAM): admin/nixbox or root/nixbox; guest/nixbox exists to
# prove the wheel gate rejects valid-but-unprivileged accounts.
```

`NIXBOX_DRY_RUN=1` swaps the command runner for a logger and downgrades
`nixos-rebuild switch` to `build`; read-only operations (syntax check, eval
check, journalctl) still run for real by design.

`NIXBOX_TERMINAL=1` exposes the web terminal (an interactive host/workload
shell over a WebSocket). It is deliberately *not* tied to dry-run — a live
shell is arbitrary user execution, not a nixbox-issued command dry-run can
neuter — so it stays behind its own opt-in and is off by default. Enabling it
publishes a root console: with auth=pam it sits behind the login like every
other route, but it remains a separate opt-in on top (a shell is a bigger
grant than buttons). The dev recipe turns it on because it is loopback-bound
and runs as the dev user.

## Testing & coverage (the bar is 100%)

Every package is tested to 100% statement coverage where achievable, and no
package sits below ~90% except `cmd/nixbox` (~80%, `os.Exit` legs). **After
any change or new feature, run `go test ./... -cover` and restore the touched
package's number before committing** — new code lands *with* its tests, not
after. A drop is a review finding, not a style preference: the uncovered lines
are exactly where the next regression hides.

Reuse the established idioms instead of inventing new harnesses:

- **HTTP handlers**: `newTestServer` + `post`/`get`/`wantRedirect` in
  `internal/server/handlers_secrets_test.go`; fault the store with
  `dropTable` (lookups 500) and `denyWrites` (RAISE triggers: reads work,
  the mutation itself fails), via a second SQLite connection.
- **Auth**: `enableAuth` swaps scripted `fakeAuthn`/`fakeAuthz` into a test
  server (`handlers_auth_test.go`); the PAM backend itself is tested with
  *real* transactions — `pam.StartConfDir` service files scripted against
  `$NIXBOX_PAM_TEST_MODULES` (pam_permit/pam_deny, exported by the
  devShell) — no root, no /etc/pam.d. Session expiry runs on the injected
  `Server.now` clock; the limiter on its own `now` field.
- **External commands**: never run real mutating commands — substitute
  `run.Runner` (scripted/recording/fail runners exist in
  `internal/nix/rebuild_test.go`, `internal/machine/manager_test.go`,
  `internal/server/handlers_test.go`).
- **Store error paths**: closed-db sweeps, corrupt `not-a-time` timestamp
  rows to fault scan loops, dropped tables for mid-transaction failures
  (`internal/store/*_test.go`).
- **Filesystem errors**: read-only dirs (chmod 0555 + cleanup), a file where
  a dir should go, rename-over-directory.
- **SSE/streaming loops**: a pre-canceled request context runs exactly one
  iteration then exits (`sseGet` in `internal/server/handlers_test.go`).
- **Async jobs**: poll the store row / `Busy()` with a deadline
  (`waitDone`/`waitIdle`), never sleep-and-hope.

Legitimate residual (don't build mock SQL drivers to paint it green):
`os.Exit` legs, unfaultable defensive returns (`LastInsertId`, `sql.Open`,
`Commit`, `crypto/rand.Read`), environment-dependent fallbacks (`os.Hostname`,
`/etc/os-release`, missing nix toolchain), and live-integration paths
(journalctl follow, WebSocket/PTY pump) — the latter belong to the verify
skill, not unit tests.
Read-only nix/sh commands (`nix-instantiate --parse`, `echo`) run for real in
tests by design, mirroring dry-run's contract.

For UI verification with a real browser (headless Chromium over CDP), follow
`.claude/skills/verify/SKILL.md`.

## Dev VM rules (violating these produces confusing failures)

- **Any code/template change requires `nix build .#vm` + VM restart.** The
  `result` symlink silently serves the last-built image. Uncommitted changes to
  git-*tracked* files ARE included in flake builds; brand-new untracked files
  are NOT (`git add -N` them).
- **Shut down with `poweroff` in the guest console or `system_powerdown` via a
  QEMU monitor** (`QEMU_OPTS="-display none -monitor unix:/tmp/sock,server,nowait"`,
  then `echo system_powerdown | nc -U -N /tmp/sock`). Killing QEMU is a power
  cut that zero-truncates freshly written guest files (nix db, container state).
  After an unclean stop, delete `testhost.qcow2`.
- After a VM restart, containers are inactive until one Apply: the VM boots the
  baked initial system, not the guest's last generation.
- The guest re-seeds `/etc/nixos` from `nix/dev-vm/configuration.nix` + a
  generated flake on every boot (see its bootstrap unit). That file is used
  both by the host to build the VM and inside the guest for self-rebuilds —
  it must keep evaluating in both contexts, which is why the generated guest
  flake imports the `qemu-vm` module.

## Architecture

**The core design problem is Nix flake purity**: a pure flake eval cannot read
`/var/lib/nixbox` at eval time. Solution: the state dir (`<stateDir>/state/`)
is itself a self-contained flake exporting `nixosModules.default`; the user's
host flake references it as a `path:` input named `nixbox-state`. Before every
rebuild, the pipeline runs `nix flake update nixbox-state --flake <hostFlake>`
to re-lock and copy fresh state into the store. Manual rebuilds without that
update get last-applied state — stale but consistent, never broken. Do not
replace this with `--impure` or a wrapper flake owning the system.

**Storage split — files vs SQLite**: anything Nix must *read* is a plain file
(source of truth: `state/workloads/<name>/workload.nix`, generated
`state/index.nix` mapping enabled workloads); anything nixbox must *query* is
a SQLite row (`workloads`, `revisions` = full snapshot per save, `jobs`,
`sessions`; migrations append-only in `internal/store/migrations.go`).
`state/` is deliberately the export format (tar it, point any flake at it).
Deleting `nixbox.db` loses history/sessions but not server config.

**Apply pipeline** (`internal/nix/rebuild.go`, invoked via
`server.startApply`): regenerate index from enabled workloads → flake update →
`nixos-rebuild switch` (builds fully before activating, so failures never
touch the running system) → record new generation on the job row. Revision IDs
are snapshotted *before* the rebuild so edits made mid-build are never marked
applied. Workload state badge = draft (never applied) / pending (latest
revision ≠ applied) / applied.

**Jobs** (`internal/jobs`): serialized (one at a time, `ErrBusy` otherwise),
run in-process, write to `logs/job-<id>.log`. SSE endpoints
(`internal/server/sse.go`) *tail the log file* rather than using an in-memory
broker — reconnects and post-restart views replay for free. Container journals
stream by piping `journalctl --follow` bound to the request context. Known
gap: a rebuild that restarts nixbox kills its own job (plan: systemd-run +
reattach; `RecoverStale` currently just marks orphans failed).

**External commands** all go through the `run.Runner` interface
(`internal/run`) so dry-run and tests can substitute; the deliberate
exceptions (validation, journalctl) are read-only and documented at their
call sites.

**Web layer**: server-rendered `html/template`, one template set per page
(layout + page) in `internal/server/server.go`; HTMX for partial swaps + a
small TypeScript layer (`web/src`, one module per feature, entry `main.ts`)
whose modules attach delegated listeners and activate on markup presence —
one bundle serves every page and keeps working inside HTMX-swapped fragments.
esbuild bundles it to `web/static/app.js`, a *gitignored build product*
(`just bundle`, the `just dev` watcher, or nix preBuild — never committed;
`tsc -p web` is the type checker, esbuild never checks). All assets embedded
via `web/embed.go` — no CDN, and no npm for app code (the committed
`codemirror.js`/`xterm.js` bundles stay npm-built under `web/editor`).
Shared fragments (`job-log`, `workload-list`) live in `layout.html`. Every
page's data embeds `baseData` (which carries the sidebar workload list).

**i18n** (`internal/i18n`, catalogs in `web/i18n/<locale>.json`): every
user-facing string goes through `{{T "key"}}` in templates or `s.t(r, key)` in
handlers — never a literal. `en.json` is the source of truth; other locales
fall back requested → base language → en → the key itself. Strings whose
English lives in Go (workload-type registry labels, template names) use
`{{TDef key fallback}}` instead, so en.json never duplicates them. `FuncMap`
binds at parse time, so `render` clones the page template per request to
rebind T to the request's locale (resolved `?lang=` → `nixbox-lang` cookie,
set by the topbar picker via `POST /lang` → `Accept-Language` → `NIXBOX_LANG`).
The two JS-rendered strings travel as translated `<body data-msg-*>`
attributes. When adding or changing a UI string, update `ro.json` alongside
`en.json` — the fallback chain means a stale ro silently shows English, which
no test catches (`TestCatalogCoverage` fails on missing, dead, or typoed keys,
but absent translations are legal). Adding a locale = drop in `<code>.json`
with a `locale.name` entry. Job logs, systemd states, and nix errors stay
untranslated by design.

## Domain constraints

- Workload names: shared `nix.ValidateName` is a path-safety rule (`[a-z0-9-]`,
  no leading/trailing `-`, ≤63 chars) — don't relax the charset. Tighter,
  type-specific caps live in `WorkloadType.ValidateName` (nixos-containers add
  the 11-char `ve-<name>` veth limit; OCI allows the full 63).
- Workload types are a registry (`internal/nix/types.go`): each `WorkloadType`
  owns its index section, static module, templates, name rule, and systemd
  unit/journal/data-dir funcs. Adding a kind (microvm, …) is one `Register`
  call plus its module string — no call site branches on the type string, and
  no schema change (`workloads.type` is free-form TEXT). `nixos-container`,
  `oci-container` (podman-backed), and `host-service` exist today.
- `host-service` is the uncontained type: its workload.nix is a NixOS module
  applied straight to the host. It has no static module (`ModuleFile: ""`) —
  renderFlake splices `index.hostServices` into `nixosModules.default` in the
  flake's *outputs* scope, because a workload's `imports` may reference
  `flakeInputs` and NixOS forbids module args in `imports` (infinite
  recursion). `builtins.functionArgs` distinguishes the `{ flakeInputs }:`
  wrapper from an ordinary module function, which passes through untouched.
  Status/lifecycle/journal follow the `<name>.service` convention — a workload
  whose units are named differently degrades to "status unavailable".
- Flake inputs (`internal/nix/flakes.go`, `internal/server/handlers_flake.go`,
  `flake_inputs` SQLite table, Flakes tab) are a pure dependency registry: name +
  flake ref + optional `follows` nixpkgs, nothing about where they're used.
  `flake.nix` is generated from the declared inputs (`WriteFlake`, mirrors
  `WriteIndex`) with *fixed* outputs (just the workload module set) — declared
  inputs are intentionally unreferenced, so Nix locks them without changing
  composition. The pipeline's `nix flake lock` step re-locks before the host
  update: adding a workload *type* never re-locks, but declaring an input (a new
  dependency) does. `Init` only seeds `flake.nix`/`flake.lock` when absent so it
  never clobbers a locked, input-bearing flake. An input's pending/locked badge
  is derived from timestamps (`applied_at` vs `updated_at`).
- Consuming an input: the flake output threads the declared inputs into every
  workload via `_module.args.flakeInputs`, and each type module composes a
  workload with `lib.toFunction (import path) { inherit flakeInputs; }`. A plain
  attrset `workload.nix` (the common case) ignores the arg; one written as
  `{ flakeInputs }: { ... }` receives the inputs and can import a module from one
  inside its own config — e.g. a nixos-container whose `config.imports` pulls in
  `flakeInputs.<name>.nixosModules.default`. `lib.toFunction` is what keeps
  existing attrset workloads working unchanged. So the Flakes tab (declare +
  lock) and consumption (reference from a workload) stay decoupled.
- Containers without `privateNetwork` share the host's network namespace:
  their ports must be opened in the *host's* firewall (the dev VM opens 8080
  for the nginx template).
- Saves with syntax errors are allowed on purpose (drafts); Apply/Dry-build
  buttons are client-side disabled while the editor has unsaved changes
  (`data-requires-saved` + guard in app.js) because rebuilds always read from
  disk, never the textarea.
- Secrets (Secrets tab, `internal/nix/secrets.go`, `internal/secret`,
  `secrets` + `secret_mounts` tables): values are age-encrypted to the host's
  SSH ed25519 key (`NIXBOX_AGE_RECIPIENT`) and persisted ONLY as ciphertext
  (`state/secrets/<name>.age` — safe in the world-readable store; plaintext in
  `workload.nix` still isn't, warn if you see it). agenix decrypts at
  activation to `/run/agenix/<name>`; a "mount" delivers that file into a
  container at the same path (nixos-container bindMounts / podman volume, per
  the type's module), host-services read it directly. The agenix flake input
  is LAZY: `renderFlake(inputs, withAgenix)` declares + imports it only while
  secrets exist, and `modules/default.nix` gates `secrets.nix` on the same
  index-derived condition — a secretless system stays input-free and offline.
  Ordering invariants: ciphertext written before its row; deletion drops the
  row only, `startApply` prunes orphaned `.age` files right after
  regenerating the index (manual rebuilds must never see a dangling
  reference).
- Auth (`internal/auth` + `internal/server/handlers_auth.go`): the default
  backend is PAM — the NixOS module generates the `nixbox` service
  (`security.pam.services.nixbox = {}`) and real Unix users log in; a valid
  password alone is *not* enough, the group gate (`NIXBOX_ALLOWED_GROUPS`,
  default wheel; uid 0 always passes) must also admit the user. Sessions are
  32-byte random cookie tokens stored only as SHA-256 in the `sessions`
  table, 7-day sliding expiry touched at most hourly. CSRF is header-based
  (stdlib `http.CrossOriginProtection` wraps the whole mux — no form tokens)
  plus SameSite=Lax; login failures cost 500 ms, PAM transactions are
  serialized, and 5 failures/min/IP answer 429. Public paths: `/login`,
  `/lang`, `/static/*`. `NIXBOX_AUTH=none` disables the layer (dev,
  reverse-proxy auth) — still loopback-only advice. Key-only admin accounts
  (`hashedPassword = "!"`) cannot password-login: set
  `users.users.<x>.hashedPassword` to use nixbox on such hosts.
