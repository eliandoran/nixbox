# nixbox command runner — `just <recipe>`, `just --list`

# Dev server (dry-run, full UI) → http://127.0.0.1:8368
dev:
    NIXBOX_DRY_RUN=1 NIXBOX_STATE_DIR=./dev-state go run ./cmd/nixbox serve

# Build and boot the disposable dev VM for real rebuilds
vm:
    nix build .#vm && ./result/bin/run-testhost-vm
