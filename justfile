# nixbox command runner — `just <recipe>`, `just --list`

# Dev server (dry-run, live assets/templates, terminal on) → http://127.0.0.1:8368
# Recompiles & restarts on Enter, Ctrl-C to quit. Pure justfile supervisor,
# so this restart logic is never part of the nixbox binary. NIXBOX_TERMINAL=1
# turns on the web terminal, safe here (loopback + dev user) but a live shell,
# so it stays opt-in everywhere else.
dev:
    #!/usr/bin/env bash
    set -u
    trap 'exit 0' INT
    tmp=$(mktemp -d); trap 'rm -rf "$tmp"' EXIT
    while true; do
        echo "▶ building…"
        if go build -o "$tmp/nixbox" ./cmd/nixbox; then
            NIXBOX_DRY_RUN=1 NIXBOX_DEV=1 NIXBOX_TERMINAL=1 NIXBOX_STATE_DIR=./dev-state "$tmp/nixbox" serve &
            pid=$!
            echo "  http://127.0.0.1:8368 (pid $pid) — Enter to rebuild & restart, Ctrl-C to quit."
            read -r || { kill "$pid" 2>/dev/null; wait "$pid" 2>/dev/null; break; }
            kill "$pid" 2>/dev/null; wait "$pid" 2>/dev/null
        else
            echo "  build failed — fix, then Enter to retry (Ctrl-C to quit)."
            read -r || break
        fi
    done

# Build and boot the disposable dev VM for real rebuilds
vm:
    nix build .#vm && ./result/bin/run-testhost-vm
