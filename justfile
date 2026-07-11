# nixbox command runner — `just <recipe>`, `just --list`

# Dev server (dry-run, live assets/templates, terminal on) → http://127.0.0.1:8368
# Recompiles & restarts on Enter, Ctrl-C to quit. Pure justfile supervisor,
# so this restart logic is never part of the nixbox binary. NIXBOX_TERMINAL=1
# turns on the web terminal, safe here (loopback + dev user) but a live shell,
# so it stays opt-in everywhere else.
dev:
    #!/usr/bin/env bash
    set -u
    tmp=$(mktemp -d); pid=""; wpid=""
    # One cleanup path for every exit (Ctrl-C, EOF, normal): kill the child
    # server and the esbuild watcher, then drop the temp dir. The bare 'exit'
    # on INT triggers it too, so Ctrl-C can never leave nixbox running.
    cleanup() { [ -n "$pid" ] && { kill "$pid" 2>/dev/null; wait "$pid" 2>/dev/null; }; [ -n "$wpid" ] && kill "$wpid" 2>/dev/null; rm -rf "$tmp"; }
    trap cleanup EXIT
    trap 'exit 0' INT
    # Dev age recipient: secrets are encrypted to an SSH pubkey, normally
    # the machine's host key — a desktop may not run sshd, and dry-run
    # never decrypts anyway, so give the dev server its own throwaway key.
    mkdir -p dev-state
    [ -f dev-state/age-recipient.pub ] || ssh-keygen -q -t ed25519 -N "" -C nixbox-dev -f dev-state/age-recipient
    # Bundle once so the go:embed of static/app.js compiles, then keep it
    # fresh in the background: dev mode serves web/ from disk, so a browser
    # refresh picks up every rebundle (inline sourcemaps → devtools show TS).
    esbuild web/src/main.ts --bundle --format=iife --outfile=web/static/app.js
    esbuild web/src/main.ts --bundle --format=iife --sourcemap=inline \
        --outfile=web/static/app.js --watch &
    wpid=$!
    while true; do
        echo "▶ building…"
        if go build -o "$tmp/nixbox" ./cmd/nixbox; then
            NIXBOX_DRY_RUN=1 NIXBOX_DEV=1 NIXBOX_TERMINAL=1 NIXBOX_AUTH=none \
                NIXBOX_STATE_DIR=./dev-state \
                NIXBOX_AGE_RECIPIENT=./dev-state/age-recipient.pub "$tmp/nixbox" serve &
            pid=$!
            echo "  http://127.0.0.1:8368 (pid $pid) — Enter to rebuild & restart, Ctrl-C to quit."
            read -r || break
            kill "$pid" 2>/dev/null; wait "$pid" 2>/dev/null; pid=""
        else
            echo "  build failed — fix, then Enter to retry (Ctrl-C to quit)."
            read -r || break
        fi
    done

# Build and boot the disposable dev VM for real rebuilds
vm:
    nix build .#vm && ./result/bin/run-testhost-vm

# One-shot bundle of the web TypeScript (web/src) → web/static/app.js, a
# gitignored build product. Run once after a fresh checkout — go build/
# vet/test fail on the missing embed until then; `just dev` re-bundles
# continuously. Mirrored by preBuild in nix/package.nix.
bundle:
    esbuild web/src/main.ts --bundle --format=iife --outfile=web/static/app.js

# Type-check web/src: esbuild only strips types, tsc enforces them.
typecheck:
    tsc -p web
