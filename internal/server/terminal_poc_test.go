package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/elian/nixbox/internal/config"
)

// TestTerminalPoC drives the shell WebSocket end to end: it dials the
// transport with a fixed /bin/sh (deterministic, no login banner — the
// host handler's login-shell resolution is machine-specific), sends a
// resize control frame and a shell command as keystrokes, and asserts the
// command's output comes back over the socket. This exercises the full
// transport + PTY path without a browser.
func TestTerminalPoC(t *testing.T) {
	s := &Server{cfg: config.Config{EnableTerminal: true}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.serveTerminal(w, r, []string{"/bin/sh"})
	}))
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	url := "ws" + strings.TrimPrefix(srv.URL, "http")
	c, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.CloseNow()

	// Resize (text frame), then a command (binary frame).
	rz, _ := json.Marshal(termResize{Rows: 24, Cols: 80})
	if err := c.Write(ctx, websocket.MessageText, rz); err != nil {
		t.Fatalf("resize write: %v", err)
	}
	if err := c.Write(ctx, websocket.MessageBinary, []byte("echo POC_OK_123\n")); err != nil {
		t.Fatalf("cmd write: %v", err)
	}

	var got strings.Builder
	for ctx.Err() == nil {
		_, data, err := c.Read(ctx)
		if err != nil {
			break
		}
		got.Write(data)
		if strings.Contains(got.String(), "POC_OK_123") {
			// Seen the echoed marker line from the shell's stdout.
			c.Write(ctx, websocket.MessageBinary, []byte("exit\n"))
			return
		}
	}
	t.Fatalf("did not see command output over the socket; got:\n%q", got.String())
}
