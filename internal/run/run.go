// Package run abstracts external command execution so every caller
// (nix pipeline, machinectl wrappers) can be exercised in tests and in
// dry-run mode without touching the system.
package run

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

type Runner interface {
	// Output runs a command and returns its stdout. Stderr is included
	// in the returned error on failure.
	Output(ctx context.Context, name string, args ...string) (string, error)
	// Stream runs a command with combined stdout+stderr written to w
	// as it is produced. Returns the exit code (0 on success).
	Stream(ctx context.Context, w io.Writer, name string, args ...string) (int, error)
}

// Exec runs commands for real.
type Exec struct{}

func (Exec) Output(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, ee.Stderr)
		}
		return "", fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
	}
	return string(out), nil
}

func (Exec) Stream(ctx context.Context, w io.Writer, name string, args ...string) (int, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = w
	cmd.Stderr = w
	fmt.Fprintf(w, "$ %s %s\n", name, strings.Join(args, " "))
	err := cmd.Run()
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			return ee.ExitCode(), nil
		}
		return -1, err
	}
	return 0, nil
}

// DryRun logs every command instead of executing it. Output-style
// queries return empty results.
type DryRun struct{}

func (DryRun) Output(ctx context.Context, name string, args ...string) (string, error) {
	return "", nil
}

func (DryRun) Stream(ctx context.Context, w io.Writer, name string, args ...string) (int, error) {
	fmt.Fprintf(w, "[dry-run] would run: %s %s\n", name, strings.Join(args, " "))
	return 0, nil
}
