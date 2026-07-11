package nix

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// writeNix drops a workload.nix with the given content into a temp dir.
func writeNix(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "workload.nix")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCheckEval(t *testing.T) {
	if _, err := exec.LookPath("nix"); err != nil {
		t.Skip("nix not available")
	}
	ctx := context.Background()

	// Plain attrset: the common case.
	if err := CheckEval(ctx, writeNix(t, `{ autoStart = true; }`)); err != nil {
		t.Errorf("attrset workload: %v", err)
	}

	// Function form consuming a flake input. attrNames must not force the
	// (absent) input, mirroring the lazy composition — this is the exact
	// shape the "Flake module" template produces.
	fn := `{ flakeInputs }: {
  autoStart = true;
  config = { ... }: {
    imports = [ flakeInputs.nixarr.nixosModules.default ];
    nixarr.enable = true;
  };
}`
	if err := CheckEval(ctx, writeNix(t, fn)); err != nil {
		t.Errorf("function workload: %v", err)
	}

	// Host-service workload written as an ordinary module function: the
	// stub args come from its own pattern, and references through them
	// (pkgs.hello) stay lazy under attrNames.
	mod := `{ config, pkgs, lib, ... }: {
  services.jellyfin.enable = true;
  environment.systemPackages = [ pkgs.hello ];
}`
	if err := CheckEval(ctx, writeNix(t, mod)); err != nil {
		t.Errorf("module-function workload: %v", err)
	}

	// A real eval error must still be reported.
	if err := CheckEval(ctx, writeNix(t, `{ x = undefinedVariable; y = x; }`)); err == nil {
		// x is lazy, so force an error at the top level instead.
		if err := CheckEval(ctx, writeNix(t, `builtins.undefinedThing`)); err == nil {
			t.Error("expected an eval error, got nil")
		}
	}
}
