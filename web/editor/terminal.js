// nixbox web terminal — xterm.js bound to the PTY WebSocket served by
// internal/server/terminal.go. Build product is committed at
// web/static/xterm.js (+ xterm.css); source excluded from the Go/Nix
// build exactly like the CodeMirror bundle.
//
// Wire protocol (must match serveTerminal):
//   keystrokes  → binary frames (UTF-8 bytes)
//   shell output → binary frames, fed straight to xterm (it decodes UTF-8,
//                  tolerating multibyte sequences split across frames)
//   resize       → text frame carrying {"rows","cols"} JSON, sent up only
import { Terminal } from "@xterm/xterm";
import { FitAddon } from "@xterm/addon-fit";
import "@xterm/xterm/css/xterm.css";

function open(el) {
  const term = new Terminal({
    cursorBlink: true,
    fontSize: 13,
    fontFamily: 'ui-monospace, "JetBrains Mono", Menlo, monospace',
    theme: { background: "#0b0e14", foreground: "#e6e6e6", cursor: "#e6e6e6" },
  });
  const fit = new FitAddon();
  term.loadAddon(fit);
  term.open(el);
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

  ws.onopen = () => {
    sendResize();
    term.focus();
  };
  ws.onmessage = (ev) => {
    term.write(ev.data instanceof ArrayBuffer ? new Uint8Array(ev.data) : ev.data);
  };
  ws.onclose = () => term.write("\r\n\x1b[31m[session closed]\x1b[0m\r\n");
  ws.onerror = () => term.write("\r\n\x1b[31m[connection error]\x1b[0m\r\n");

  // Keystrokes as binary frames.
  term.onData((data) => {
    if (ws.readyState === WebSocket.OPEN) ws.send(enc.encode(data));
  });
  // Keep the PTY window size in sync with the rendered grid.
  term.onResize(sendResize);
  const ro = new ResizeObserver(() => {
    try {
      fit.fit();
    } catch {}
  });
  ro.observe(el);
}

function init() {
  const el = document.getElementById("terminal");
  if (el && !el.dataset.mounted) {
    el.dataset.mounted = "1";
    open(el);
  }
}

// `defer` runs this after parse; fall back to the event if loaded earlier.
if (document.readyState === "loading") document.addEventListener("DOMContentLoaded", init);
else init();
