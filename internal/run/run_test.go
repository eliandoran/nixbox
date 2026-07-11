package run

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestExecOutput(t *testing.T) {
	ctx := context.Background()

	out, err := Exec{}.Output(ctx, "echo", "hello")
	if err != nil || out != "hello\n" {
		t.Fatalf("Output = %q, %v", out, err)
	}

	// A failing command's stderr rides along in the error.
	_, err = Exec{}.Output(ctx, "sh", "-c", "echo broken >&2; exit 1")
	if err == nil || !strings.Contains(err.Error(), "broken") {
		t.Errorf("exit error = %v, want stderr included", err)
	}

	// A missing binary is not an ExitError.
	if _, err := (Exec{}).Output(ctx, "definitely-no-such-binary-xyz"); err == nil {
		t.Error("missing binary: want error")
	}
}

func TestExecStream(t *testing.T) {
	ctx := context.Background()

	var buf bytes.Buffer
	code, err := Exec{}.Stream(ctx, &buf, "sh", "-c", "echo out; echo err >&2")
	if err != nil || code != 0 {
		t.Fatalf("Stream = %d, %v", code, err)
	}
	// The command line is echoed, and stdout+stderr are combined.
	for _, want := range []string{"$ sh -c", "out\n", "err\n"} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("stream missing %q:\n%s", want, buf.String())
		}
	}

	// A nonzero exit is a code, not an error.
	code, err = Exec{}.Stream(ctx, &buf, "sh", "-c", "exit 3")
	if err != nil || code != 3 {
		t.Errorf("exit 3 = %d, %v", code, err)
	}

	// A missing binary is an error with the -1 sentinel.
	code, err = Exec{}.Stream(ctx, &buf, "definitely-no-such-binary-xyz")
	if err == nil || code != -1 {
		t.Errorf("missing binary = %d, %v", code, err)
	}
}

func TestDryRun(t *testing.T) {
	ctx := context.Background()

	out, err := DryRun{}.Output(ctx, "systemctl", "reboot")
	if err != nil || out != "" {
		t.Errorf("DryRun.Output = %q, %v", out, err)
	}

	var buf bytes.Buffer
	code, err := DryRun{}.Stream(ctx, &buf, "nixos-rebuild", "switch")
	if err != nil || code != 0 {
		t.Fatalf("DryRun.Stream = %d, %v", code, err)
	}
	if !strings.Contains(buf.String(), "[dry-run] would run: nixos-rebuild switch") {
		t.Errorf("log line = %q", buf.String())
	}
}
