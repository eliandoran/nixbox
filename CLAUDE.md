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

# Dev server — safe on any machine, full UI works:
NIXBOX_DRY_RUN=1 NIXBOX_STATE_DIR=./dev-state go run ./cmd/nixbox serve
# → http://127.0.0.1:8368 (NIXBOX_LISTEN=127.0.0.1:PORT to change)

# Dev VM — for REAL rebuilds; never test real applies on the host:
nix build .#vm && ./result/bin/run-testhost-vm
# → UI http://localhost:18368, nginx template container http://localhost:18080
```

`NIXBOX_DRY_RUN=1` swaps the command runner for a logger and downgrades
`nixos-rebuild switch` to `build`; read-only operations (syntax check, eval
check, journalctl) still run for real by design.

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

- Container names: `[a-z0-9-]`, max 11 chars (`ve-<name>` veth interface
  limit) — enforced by `nix.ValidateName`, don't relax it.
- `workloads.type` exists to add OCI/microvm workloads later: new type = new
  branch in index generation + new static module in `state/modules/` — no
  schema change. Only `nixos-container` exists today.
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
