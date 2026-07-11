# nixbox

A self-hosted web interface for managing a NixOS server's declarative
containers — Portainer/Proxmox in spirit, but each container's
configuration stays a raw Nix expression. nixbox handles the glue:
composing containers into your system flake, running rebuilds, updating
flake inputs, and showing status and logs.

**Status: early development (milestone 1 of 4).** The walking skeleton
works: dashboard, rebuild pipeline with live log streaming, generated
state flake. Container CRUD from the browser lands next.

## How it works

nixbox owns a small generated flake at `/var/lib/nixbox/state` that
exports `nixosModules.default`, mapping each enabled workload's
`workload.nix` into `containers.<name>`. Your host flake references it
as a `path:` input, staying the single source of truth for the system:

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
$ go test ./...
$ just dev
```

The devShell provides `just` as a command runner (`just --list` shows
the recipes). `just dev` starts the dry-run server
(`NIXBOX_DRY_RUN=1 NIXBOX_STATE_DIR=./dev-state go run ./cmd/nixbox serve`);
`NIXBOX_DRY_RUN` logs commands instead of executing them, so the full UI
can be exercised without touching the system. Real rebuilds should only
be tested in a VM.

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
