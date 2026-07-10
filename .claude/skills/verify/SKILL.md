---
name: verify
description: Build, launch, and drive nixbox end-to-end to verify a change against the real UI.
---

# Verifying nixbox changes

## Launch (safe, no system mutation)

```bash
NIXBOX_DRY_RUN=1 NIXBOX_STATE_DIR=./dev-state NIXBOX_LISTEN=127.0.0.1:8471 \
  nix develop -c go run ./cmd/nixbox serve
```

- `NIXBOX_DRY_RUN=1` logs commands instead of executing them and downgrades
  `nixos-rebuild switch` to `build` — the whole UI (including the Rebuild
  button) is safe to click.
- Use a non-default port to avoid colliding with a locally running instance
  (default is 8368).
- `./dev-state` already has a seeded SQLite db + state flake; jobs history
  accumulates there harmlessly.

## Drive the UI

Headless Chromium + CDP works well on this machine (no Playwright installed;
node ≥ 24 has native `WebSocket` and `fetch`):

```bash
chromium --headless --remote-debugging-port=9223 \
  --user-data-dir=$(mktemp -d) --no-first-run about:blank &
# PUT (not GET) to open a page target:
# fetch('http://127.0.0.1:9223/json/new?about:blank', {method:'PUT'})
```

- Enable `Page` + `Runtime`, set `Emulation.setDeviceMetricsOverride`
  (1280×860), navigate, `Runtime.evaluate`, `Page.captureScreenshot`.
- Theme: default headless prefers-color-scheme is light; emulate dark with
  `Emulation.setEmulatedMedia {features:[{name:'prefers-color-scheme',value:'dark'}]}`.
  Manual override lives in `localStorage["nixbox-theme"]` → `<html data-theme>`.
- The Rebuild flow is htmx: click the button, the job-log fragment swaps in
  and `app.js` tails an SSE stream (`/events/jobs/<id>`) into `#job-log`.
  Dry-run jobs finish in ~1s, so a "running" badge is hard to catch live.

## Flows worth driving

- Dashboard `/`, System `/system` (click Rebuild, watch log stream + status
  badge), New container `/workloads/new` (form + template picker),
  workload detail (editor textarea, enable/disable, danger zone).
- Theme change? Check both schemes; status badges (`--ok/--bad/--warn` as
  text) and `button.primary` (`--on-accent`) are the contrast-sensitive spots.
