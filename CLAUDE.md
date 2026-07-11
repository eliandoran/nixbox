# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

nixbox is a self-hosted web UI (Go + HTMX, single static binary) for managing a
NixOS server's declarative containers. Users edit each container's config as a
raw Nix expression in the browser; nixbox composes them into the system flake,
runs `nixos-rebuild`, and streams status/logs. It runs as root on the machine
it manages. Milestones 1–2 plus container logs are done; remaining roadmap:
generations/rollback, flake input updates, export, then auth (see git log).

## Commands

```bash
go build ./... && go vet ./... && go test ./...   # standard check
go test ./internal/nix -run TestWriteIndex        # single test
nix build .#nixbox                                # package (update vendorHash in nix/package.nix when go.mod changes)

# CodeMirror Nix editor bundle — vendored build product, committed at
# web/static/codemirror.js and embedded via //go:embed. Regenerate only
# when the editor source or its deps change (needs nodejs + esbuild from
# the devShell); npm is NOT part of go/nix build. Then commit the result.
(cd web/editor && npm ci && npm run build)

# Dev server — safe on any machine, full UI works (`just dev` wraps this):
NIXBOX_DRY_RUN=1 NIXBOX_TERMINAL=1 NIXBOX_STATE_DIR=./dev-state go run ./cmd/nixbox serve
# → http://127.0.0.1:8368 (NIXBOX_LISTEN=127.0.0.1:PORT to change)

# Dev VM — for REAL rebuilds; never test real applies on the host:
nix build .#vm && ./result/bin/run-testhost-vm
# → UI http://localhost:18368, nginx template container http://localhost:18080
```

`NIXBOX_DRY_RUN=1` swaps the command runner for a logger and downgrades
`nixos-rebuild switch` to `build`; read-only operations (syntax check, eval
check, journalctl) still run for real by design.

`NIXBOX_TERMINAL=1` exposes the web terminal (an interactive host/workload
shell over a WebSocket). It is deliberately *not* tied to dry-run — a live
shell is arbitrary user execution, not a nixbox-issued command dry-run can
neuter — so it stays behind its own opt-in and is off by default. Enabling it
is equivalent to publishing a root console; only safe once auth (milestone 4)
lands. The dev recipe turns it on because it is loopback-bound and runs as the
dev user.

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
(layout + page) in `internal/server/server.go`; HTMX for partial swaps +
vanilla-JS EventSource for SSE (`web/static/app.js`); all assets embedded via
`web/embed.go` — no CDN, no build step. Shared fragments (`job-log`,
`workload-list`) live in `layout.html`. Every page's data embeds `baseData`
(which carries the sidebar workload list).

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
- Secrets in `workload.nix` end up world-readable in the Nix store — known
  limitation, planned agenix/sops-style feature; warn, don't work around.
- No auth yet (milestone 4): bind loopback only; treat UI access as root.
