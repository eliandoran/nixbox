package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/elian/nixbox/internal/config"
)

// setArgs rewrites os.Args for a main() invocation and restores it after
// the test. Only subcommands that return (help, successful init) can be
// driven this way — the usage/error legs call os.Exit and stay uncovered.
func setArgs(t *testing.T, args ...string) {
	t.Helper()
	orig := os.Args
	os.Args = append([]string{"nixbox"}, args...)
	t.Cleanup(func() { os.Args = orig })
}

func TestMainHelp(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		setArgs(t, arg)
		main() // must return, not exit
	}
}

func TestMainInit(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("NIXBOX_STATE_DIR", dir)
	setArgs(t, "init")
	main()
	for _, f := range []string{"flake.nix", "flake.lock", "index.nix", "modules/default.nix"} {
		if _, err := os.Stat(filepath.Join(dir, "state", f)); err != nil {
			t.Errorf("init did not seed %s: %v", f, err)
		}
	}
	// Idempotent: a second init over the same dir succeeds.
	main()
}

// testConfig is a serve()-able config rooted in a temp dir, with a listen
// address that fails at bind time so ListenAndServe returns instead of
// blocking — every wiring step before it runs for real.
func testConfig(t *testing.T) config.Config {
	t.Helper()
	return config.Config{
		Listen:       "127.0.0.1:-1",
		StateDir:     t.TempDir(),
		HostFlake:    "/etc/nixos",
		HostAttr:     "test",
		AgeRecipient: "/nonexistent.pub",
		DryRun:       true,
		Lang:         "en",
	}
}

func TestServeWiring(t *testing.T) {
	// The full path: flake init, store, jobs, pipeline, server — then the
	// deliberately-invalid listen address fails last.
	err := serve(testConfig(t))
	if err == nil {
		t.Fatal("invalid listen address: want error")
	}

	// The dry-run flag selects the logging runner; without it the real
	// runner is wired. Same invalid-listen exit either way.
	cfg := testConfig(t)
	cfg.DryRun = false
	if err := serve(cfg); err == nil {
		t.Fatal("non-dry-run wiring: want listen error")
	}
}

func TestServeFailures(t *testing.T) {
	// State flake init fails when the state dir is a file.
	cfg := testConfig(t)
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cfg.StateDir = blocker
	if err := serve(cfg); err == nil {
		t.Error("state dir over a file: want error")
	}

	// The database cannot open when its path is a directory.
	cfg = testConfig(t)
	if err := os.MkdirAll(cfg.DBPath(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := serve(cfg); err == nil {
		t.Error("db path as directory: want error")
	}

	// The jobs manager cannot create its logs dir over a file.
	cfg = testConfig(t)
	if err := os.WriteFile(cfg.LogsDir(), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := serve(cfg); err == nil {
		t.Error("logs dir over a file: want error")
	}

	// server.New fails in dev mode without a ./web dir on disk.
	cfg = testConfig(t)
	cfg.Dev = true
	if err := serve(cfg); err == nil {
		t.Error("dev mode without ./web: want error")
	}
}
