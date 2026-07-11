# nixbox

A self-hosted web interface for managing a NixOS server's declarative
workloads — Portainer/Proxmox in spirit, but each workload's
configuration stays a raw Nix expression. nixbox handles the glue:
composing workloads into your system flake, running rebuilds, declaring
and pinning flake inputs, and showing status and logs.

**Status: usable, pre-1.0.** What works today:

- **Workloads** of three kinds — NixOS containers, OCI containers
  (podman-backed), and host services (plain NixOS modules applied to the
  host) — created from templates, edited as Nix in a CodeMirror editor
  with syntax/eval checks, enabled, applied, renamed, destroyed. Every
  save is a revision that can be restored.
- **Apply pipeline** with live log streaming; `nixos-rebuild switch`
  builds fully before activating, so a failed apply never touches the
  running system. Per-workload status, journal streaming, metrics,
  lifecycle buttons, and an interactive shell.
- **Secrets**: values age-encrypted to the host's SSH key, decrypted by
  agenix at activation, mountable into containers.
- **Flake inputs**: a registry of external flakes, locked on apply,
  consumable from any workload.
- **Auth**: PAM login against real system accounts, with a group gate
  on top (a valid password alone is not enough).
- An opt-in web terminal on the host, and a localized UI (English and
  Romanian).

Remaining roadmap: generation history and rollback, updating locked
flake inputs, state export.

## How it works

nixbox owns a small generated flake at `/var/lib/nixbox/state` that
exports `nixosModules.default`, mapping each enabled workload's
`workload.nix` into the host configuration (`containers.<name>` for
NixOS containers, a podman unit for OCI containers, the module itself
for host services). Your host flake references it as a `path:` input,
staying the single source of truth for the system:

```nix
{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    nixbox.url = "github:elian/nixbox";
    nixbox-state.url = "path:/var/lib/nixbox/state";
  };
  outputs = { self, nixpkgs, nixbox, nixbox-state, ... }: {
    nixosConfigurations.myhost = nixpkgs.lib.nixosSystem {
      modules = [
        ./configuration.nix
        nixbox.nixosModules.nixbox
        nixbox-state.nixosModules.default
        { services.nixbox.enable = true; }
      ];
    };
  };
}
```

Before every rebuild, nixbox runs `nix flake update nixbox-state` so the
lock always points at the current state. A manual `nixos-rebuild`
without that update still works — it just uses the last-applied state.

### Flake inputs

The **Flakes** tab is a registry of external flakes declared as inputs of
the managed state flake — a pure dependency list (name + flake ref). It is
deliberately separate from where an input is *used*: defining an input
makes it available and, on apply, fetched and pinned into `state/flake.lock`
(the pipeline runs `nix flake lock` before the rebuild, so a bad ref fails
the apply before `nixos-rebuild`, never touching the running system).
Wiring an input into a workload or the host is a separate concern.

If an input opts to *follow a shared nixpkgs*, the state flake declares its
own `nixpkgs` input. To collapse that onto the host's nixpkgs (one copy in
the closure, no version skew), point the state input's nixpkgs at yours in
the host flake:

```nix
inputs.nixbox-state = {
  url = "path:/var/lib/nixbox/state";
  inputs.nixpkgs.follows = "nixpkgs";
};
```

Setup is two steps because the `path:` input must exist first:

1. Run `sudo nixbox init` (or enable only `services.nixbox` and rebuild
   once) to seed `/var/lib/nixbox/state`.
2. Add the `nixbox-state` input + module and rebuild again.

## Development

```console
$ nix develop
$ just bundle     # once after checkout: web TS → web/static/app.js (gitignored, go:embed needs it)
$ go test ./...
$ just dev
```

The devShell provides `just` as a command runner (`just --list` shows
the recipes). The web frontend is TypeScript in `web/src`, bundled by
esbuild into `web/static/app.js` — a build product, never committed —
so `go build`/`test` fail on the missing embed until the first
`just bundle` (`just dev` and the nix package bundle automatically;
`just typecheck` runs `tsc`, which esbuild doesn't). `just dev` starts
the dry-run server
(`NIXBOX_DRY_RUN=1 NIXBOX_TERMINAL=1 NIXBOX_AUTH=none NIXBOX_STATE_DIR=./dev-state go run ./cmd/nixbox serve`);
`NIXBOX_DRY_RUN` logs commands instead of executing them, so the full UI
can be exercised without touching the system, `NIXBOX_TERMINAL=1`
enables the web terminal (off by default; it exposes a live shell), and
`NIXBOX_AUTH=none` skips the login screen (the packaged default is PAM).
Real rebuilds should only be tested in a VM.

### Dev VM (real rebuilds, disposable)

```console
$ just vm
```

(equivalently `nix build .#vm && ./result/bin/run-testhost-vm`).

Boots a throwaway NixOS VM running nixbox for real. Open
http://localhost:18368, create a container, press Apply — an actual
`nixos-rebuild switch` runs inside the VM. The nginx template is
reachable at http://localhost:18080 once applied. The VM shares the
host's `/nix/store` read-only, so rebuilds inside are mostly cache-hot.
State lives in `testhost.qcow2` in your working directory; delete it
for a fresh machine. Console auto-login as root (password `nixbox`),
headless via `QEMU_OPTS="-display none"`.

The web UI asks for a login (real PAM inside the VM): use `admin` /
`nixbox` (a wheel member) or `root` / `nixbox`. A third account,
`guest` / `nixbox`, has a valid password but no admin group — it exists
to demonstrate that authentication alone does not grant access.

**Shut the VM down gracefully** — run `poweroff` in the console (or
send `system_powerdown` to a QEMU monitor). Killing QEMU or closing
its window is a power cut: files written inside the guest (container
state, the nix db) can be truncated to zeros, after which rebuilds and
containers fail in confusing ways. If that happens, delete
`testhost.qcow2` and start fresh.

## Configuration

Set through the `services.nixbox` NixOS module (see `nix/module.nix`):
listen address (default `127.0.0.1:8368`), host flake path (default
`/etc/nixos`), and the `nixosConfigurations` attribute to rebuild
(default: the hostname).

### Authentication

Signing in is on by default (`services.nixbox.auth = "pam"`): the module
generates a standard `nixbox` PAM service, so you log in with a real
system account — nixbox stores no credentials of its own. A valid
password alone is not enough: the account must also belong to one of
`services.nixbox.allowedGroups` (default `[ "wheel" ]`; root is always
allowed), because nixbox is root-equivalent by design.

Two caveats worth knowing:

- **Key-only admin accounts cannot sign in.** If your user has no Unix
  password (SSH keys only, `hashedPassword = "!"`), PAM has nothing to
  verify. Set `users.users.<you>.hashedPassword` (e.g. from
  `mkpasswd -m yescrypt`) to use nixbox on such a host. This puts a
  password *hash* in the world-readable store — standard NixOS practice
  for `hashedPassword`, but worth being deliberate about.
- `auth = "none"` disables the login entirely — only for trusted
  loopback use or behind a reverse proxy that authenticates for you
  (the module warns if you combine it with `openFirewall`).

Brute force is blunted in depth: failed logins cost half a second, PAM
checks are serialized, and five failures per minute per client IP answer
`429`. Sessions are 7-day sliding cookies (HttpOnly, SameSite=Lax) whose
tokens are stored only hashed; cross-site request forgery is rejected
from the `Sec-Fetch-Site`/`Origin` headers, with no form tokens needed.
