package nix

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

// scriptedRunner returns a scripted (code, err) per Stream call, in order,
// and records every command line — enough to drive Rebuild through each of
// its exits without touching the system.
type scriptedRunner struct {
	codes []int
	errs  []error
	calls [][]string
}

func (r *scriptedRunner) Output(context.Context, string, ...string) (string, error) {
	return "", nil
}

func (r *scriptedRunner) Stream(_ context.Context, _ io.Writer, name string, args ...string) (int, error) {
	i := len(r.calls)
	r.calls = append(r.calls, append([]string{name}, args...))
	var code int
	var err error
	if i < len(r.codes) {
		code = r.codes[i]
	}
	if i < len(r.errs) {
		err = r.errs[i]
	}
	return code, err
}

func testPipeline(r *scriptedRunner, dryRun bool) *Pipeline {
	return &Pipeline{
		Runner: r, HostFlake: "/host", HostAttr: "test",
		StateDir: "/state", StateInputName: "nixbox-state", DryRun: dryRun,
	}
}

func TestRebuildSequence(t *testing.T) {
	r := &scriptedRunner{}
	var log bytes.Buffer
	code, _, err := testPipeline(r, false).Rebuild(context.Background(), &log, ModeSwitch)
	if err != nil || code != 0 {
		t.Fatalf("Rebuild = %d, %v", code, err)
	}
	want := [][]string{
		{"nix", "flake", "lock", "/state"},
		{"nix", "flake", "update", "nixbox-state", "--flake", "/host"},
		{"nixos-rebuild", "switch", "--flake", "/host#test"},
	}
	if len(r.calls) != len(want) {
		t.Fatalf("calls = %v", r.calls)
	}
	for i := range want {
		if strings.Join(r.calls[i], " ") != strings.Join(want[i], " ") {
			t.Errorf("call %d = %v, want %v", i, r.calls[i], want[i])
		}
	}
}

func TestRebuildDryRunDowngrade(t *testing.T) {
	r := &scriptedRunner{}
	var log bytes.Buffer
	if _, _, err := testPipeline(r, true).Rebuild(context.Background(), &log, ModeSwitch); err != nil {
		t.Fatal(err)
	}
	if got := r.calls[2][1]; got != "build" {
		t.Errorf("dry-run mode = %q, want build", got)
	}
	if !strings.Contains(log.String(), "dry-run") {
		t.Error("downgrade note missing from log")
	}

	// ModeBuild is already safe and passes through without the note.
	r = &scriptedRunner{}
	log.Reset()
	if _, _, err := testPipeline(r, true).Rebuild(context.Background(), &log, ModeBuild); err != nil {
		t.Fatal(err)
	}
	if got := r.calls[2][1]; got != "build" {
		t.Errorf("build mode = %q", got)
	}
	if strings.Contains(log.String(), "dry-run") {
		t.Error("unexpected downgrade note for ModeBuild")
	}
}

func TestRebuildFailures(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name     string
		codes    []int
		errs     []error
		wantCode int
		wantErr  string // substring of the wrapped error; "" for none
	}{
		{"lock error", nil, []error{boom}, -1, "nix flake lock"},
		{"lock exit code", []int{3}, nil, 3, ""},
		{"update error", nil, []error{nil, boom}, -1, "nix flake update"},
		{"update exit code", []int{0, 4}, nil, 4, ""},
		{"rebuild error", nil, []error{nil, nil, boom}, -1, "nixos-rebuild"},
		{"rebuild exit code", []int{0, 0, 5}, nil, 5, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &scriptedRunner{codes: tt.codes, errs: tt.errs}
			var log bytes.Buffer
			code, gen, err := testPipeline(r, false).Rebuild(context.Background(), &log, ModeSwitch)
			if code != tt.wantCode {
				t.Errorf("code = %d, want %d", code, tt.wantCode)
			}
			if gen != nil {
				t.Errorf("generation on failure: %v", *gen)
			}
			if tt.wantErr == "" && err != nil {
				t.Errorf("err = %v, want nil", err)
			}
			if tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)) {
				t.Errorf("err = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

// TestCurrentGeneration tolerates both environments: on a NixOS host the
// profile link resolves to a positive generation; elsewhere the readlink
// error path runs.
func TestCurrentGeneration(t *testing.T) {
	gen, err := CurrentGeneration()
	if err == nil && gen <= 0 {
		t.Errorf("generation = %d, want positive", gen)
	}
}
