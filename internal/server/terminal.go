// PoC web terminal: an interactive shell in the browser, bridged to a
// PTY over a WebSocket (coder/websocket + creack/pty). Two entry points
// share the same byte-pump: a host shell (/terminal/ws) and a
// per-workload shell (/workloads/{name}/terminal, driven by the type's
// ShellArgs).
//
// Wire protocol, matching web/editor/terminal.js:
//   - binary frames  → keystrokes (browser→PTY) and shell output (PTY→browser)
//   - text frames     → a {"rows","cols"} JSON resize control message, up only
//
// The PTY lives exactly as long as the socket: closing the tab cancels
// the request context, which kills the shell — the same lifetime rule as
// the journal stream in sse.go.
//
// NOT production-ready: no auth, same-origin only, no session persistence
// across a nixbox restart. It deliberately bypasses run.Runner (an
// interactive PTY has no dry-run analogue), so it is gated behind its own
// explicit opt-in (NIXBOX_TERMINAL / cfg.EnableTerminal) rather than
// DryRun — see config.Config.EnableTerminal for why the two are separate.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"os/exec"

	"github.com/coder/websocket"
	"github.com/creack/pty"
)

// termResize is the only control message the browser sends (text frame).
// Keystrokes travel as raw bytes on binary frames and never need decoding.
type termResize struct {
	Rows uint16 `json:"rows"`
	Cols uint16 `json:"cols"`
}

// handleTerminalPage renders the full-screen host terminal.
func (s *Server) handleTerminalPage(w http.ResponseWriter, r *http.Request) {
	type data struct {
		baseData
		Enabled bool
	}
	s.renderPage(w, r, "terminal", data{
		baseData: s.base(r, "Terminal", "terminal"),
		Enabled:  s.cfg.EnableTerminal,
	})
}

// handleHostTerminal opens an interactive login shell on the host and
// bridges it to the browser. This is the SSH-like console.
func (s *Server) handleHostTerminal(w http.ResponseWriter, r *http.Request) {
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	s.serveTerminal(w, r, []string{shell, "-l"})
}

// handleWorkloadTerminal opens a shell inside a running workload, using
// the argv the workload type advertises (machinectl shell / podman exec).
func (s *Server) handleWorkloadTerminal(w http.ResponseWriter, r *http.Request) {
	wl, ok := s.lookupWorkload(w, r)
	if !ok {
		return
	}
	wt := workloadType(wl.Type)
	if wt.ShellArgs == nil {
		http.Error(w, "workload type has no shell", http.StatusBadRequest)
		return
	}
	s.serveTerminal(w, r, wt.ShellArgs(wl.Name))
}

// serveTerminal upgrades the request to a WebSocket, starts argv on a
// PTY, and pumps bytes both ways until either side closes. The process is
// bound to the request context so a dropped connection tears the shell
// down.
func (s *Server) serveTerminal(w http.ResponseWriter, r *http.Request, argv []string) {
	if !s.cfg.EnableTerminal {
		http.Error(w, "terminal disabled (set NIXBOX_TERMINAL=1 to enable)", http.StatusForbidden)
		return
	}

	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		// Same-origin only. A real build must check Origin against the
		// configured host once auth exists (milestone 4).
		OriginPatterns: []string{r.Host},
	})
	if err != nil {
		return // Accept already wrote the failure
	}
	defer c.CloseNow()
	// Keystrokes and resize messages are tiny; keep the default small read
	// limit (it only bounds browser→server frames, not shell output).

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	tty, err := pty.Start(cmd)
	if err != nil {
		c.Close(websocket.StatusInternalError, "cannot start shell")
		return
	}
	defer tty.Close()
	defer cmd.Wait()

	// PTY → browser: shell output as binary frames. On any error, cancel
	// the context so the read loop below unblocks and the process dies.
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := tty.Read(buf)
			if n > 0 {
				if werr := c.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
					cancel()
					return
				}
			}
			if err != nil {
				cancel()
				return
			}
		}
	}()

	// browser → PTY: binary frames are keystrokes; text frames are resize
	// control messages.
	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			var ce websocket.CloseError
			if errors.As(err, &ce) || errors.Is(err, context.Canceled) {
				return
			}
			return
		}
		switch typ {
		case websocket.MessageBinary:
			if _, err := tty.Write(data); err != nil {
				return
			}
		case websocket.MessageText:
			var m termResize
			if json.Unmarshal(data, &m) == nil && m.Rows > 0 && m.Cols > 0 {
				_ = pty.Setsize(tty, &pty.Winsize{Rows: m.Rows, Cols: m.Cols})
			}
		}
	}
}
