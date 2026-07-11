// nixbox web terminal — xterm.js bound to the PTY WebSocket served by
// internal/server/terminal.go. Build product is committed at
// web/static/xterm.js (+ xterm.css); source excluded from the Go/Nix
// build exactly like the CodeMirror bundle.
//
// Two mount modes, both reading the ws path from `data-terminal-ws`:
//   - #terminal            → auto-connects on load (the full-page host shell)
//   - [data-terminal-open] → a button that reveals + connects its target
//                            terminal element on click (the per-workload shell)
//
// Wire protocol (must match serveTerminal):
//   keystrokes  → binary frames (UTF-8 bytes)
//   shell output → binary frames, fed straight to xterm (it decodes UTF-8,
//                  tolerating multibyte sequences split across frames)
//   resize       → text frame carrying {"rows","cols"} JSON, sent up only
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import { WebglAddon } from "@xterm/addon-webgl";
import "@xterm/xterm/css/xterm.css";

// openSession connects `el` to its WebSocket and wires a live xterm into
// it. Any previous session on the same element is torn down first, so it
// is safe to call again to reconnect. `onClose` fires when the socket
// closes (peer gone, shell exited, or start failure).
function openSession(el, onClose) {
  if (el._teardown) el._teardown();
  el.innerHTML = "";

  const term = new Terminal({
    cursorBlink: true,
    fontSize: 13,
    fontFamily: 'ui-monospace, "JetBrains Mono", Menlo, monospace',
    theme: { background: "#0b0e14", foreground: "#e6e6e6", cursor: "#e6e6e6" },
  });
  const fit = new FitAddon();
  term.loadAddon(fit);
  term.open(el);
  // WebGL renderer for pixel-perfect box drawing: the default DOM renderer
  // draws box/block characters as font glyphs, which leave sub-pixel seams
  // between cells; WebGL renders them itself (customGlyphs, on by default).
  // Fall back silently to the DOM renderer where WebGL is unavailable.
  try {
    const webgl = new WebglAddon();
    webgl.onContextLoss(() => webgl.dispose());
    term.loadAddon(webgl);
  } catch {}
  fit.fit();

  const proto = location.protocol === "https:" ? "wss:" : "ws:";
  const ws = new WebSocket(`${proto}//${location.host}${el.dataset.terminalWs}`);
  ws.binaryType = "arraybuffer";
  const enc = new TextEncoder();

  const sendResize = () => {
    if (ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify({ rows: term.rows, cols: term.cols }));
    }
  };

  let done = false;
  const finish = (msg) => {
    if (done) return;
    done = true;
    term.write(msg);
    if (onClose) onClose();
  };

  ws.onopen = () => {
    sendResize();
    term.focus();
  };
  ws.onmessage = (ev) => {
    term.write(ev.data instanceof ArrayBuffer ? new Uint8Array(ev.data) : ev.data);
  };
  ws.onclose = () => finish("\r\n\x1b[31m[session closed]\x1b[0m\r\n");
  ws.onerror = () => finish("\r\n\x1b[31m[connection error]\x1b[0m\r\n");

  term.onData((data) => {
    if (ws.readyState === WebSocket.OPEN) ws.send(enc.encode(data));
  });
  term.onResize(sendResize);
  const ro = new ResizeObserver(() => {
    try {
      fit.fit();
    } catch {}
  });
  ro.observe(el);

  el._teardown = () => {
    ro.disconnect();
    try {
      ws.close();
    } catch {}
    term.dispose();
    el._teardown = null;
  };
}

function init() {
  // Full-page host terminal: connect immediately.
  const host = document.getElementById("terminal");
  if (host && host.dataset.terminalWs && !host.dataset.mounted) {
    host.dataset.mounted = "1";
    openSession(host);
  }

  // Button-driven terminals (per workload): reveal + connect on click, and
  // offer a reconnect once the session ends.
  for (const btn of document.querySelectorAll("[data-terminal-open]")) {
    if (btn.dataset.wired) continue;
    btn.dataset.wired = "1";
    const el = document.getElementById(btn.dataset.terminalOpen);
    if (!el) continue;
    btn.addEventListener("click", () => {
      el.hidden = false;
      btn.disabled = true;
      openSession(el, () => {
        btn.disabled = false;
        btn.textContent = "Reconnect";
      });
    });
  }
}

// `defer` runs this after parse; fall back to the event if loaded earlier.
if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", init);
else init();
