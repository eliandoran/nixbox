package nix

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRenderFlakeAgenix: with secrets present the flake declares the
// built-in agenix pin (following the shared nixpkgs, extras pruned) and
// imports its NixOS module next to the workload module set.
func TestRenderFlakeAgenix(t *testing.T) {
	got := renderFlake(nil, true)
	for _, want := range []string{
		`nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";`,
		`url = "github:ryantm/agenix";`,
		`inputs.nixpkgs.follows = "nixpkgs";`,
		`inputs.darwin.follows = "";`,
		`imports = [ ./modules/default.nix inputs.agenix.nixosModules.default ] ++ (map`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("renderFlake(nil, true) missing %q:\n%s", want, got)
		}
	}
}

// TestRenderFlakeAgenixUserInput: a user-declared input named agenix wins
// over the built-in pin — declared once, still imported.
func TestRenderFlakeAgenixUserInput(t *testing.T) {
	got := renderFlake([]FlakeInput{{Name: "agenix", URL: "github:owner/agenix-fork"}}, true)
	if strings.Contains(got, "github:ryantm/agenix") {
		t.Errorf("built-in pin emitted despite user-declared agenix:\n%s", got)
	}
	if !strings.Contains(got, `agenix.url = "github:owner/agenix-fork";`) {
		t.Errorf("user agenix input missing:\n%s", got)
	}
	if !strings.Contains(got, "inputs.agenix.nixosModules.default") {
		t.Errorf("agenix module not imported:\n%s", got)
	}
}

// TestRenderFlakeNoAgenixWithoutSecrets: the lazy property — no secrets,
// no agenix anywhere, even with other inputs declared.
func TestRenderFlakeNoAgenixWithoutSecrets(t *testing.T) {
	got := renderFlake([]FlakeInput{{Name: "svc", URL: "github:owner/svc"}}, false)
	if strings.Contains(got, "agenix") {
		t.Errorf("agenix leaked into a secretless flake:\n%s", got)
	}
}

func TestWriteIndexSecrets(t *testing.T) {
	f := &StateFlake{Dir: t.TempDir()}
	if err := f.Init(); err != nil {
		t.Fatal(err)
	}
	err := f.WriteIndex(
		[]IndexEntry{
			{Name: "web", Type: WorkloadTypeContainer},
			{Name: "app", Type: WorkloadTypeOCI},
		},
		[]IndexSecret{
			{
				Name: "db-pass", Owner: "root", Group: "root", Mode: "0400",
				Mounts: map[string][]string{
					"containers":    {"web"},
					"ociContainers": {"app"},
					"hostServices":  {}, // empty: not emitted
				},
			},
			{Name: "api-key", Owner: "nginx", Group: "nginx", Mode: "0440"},
		})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(f.Dir, "index.nix"))
	if err != nil {
		t.Fatal(err)
	}
	want := `  secrets = {
    api-key = {
      file = ./secrets/api-key.age;
      owner = "nginx";
      group = "nginx";
      mode = "0440";
      mounts = {
      };
    };
    db-pass = {
      file = ./secrets/db-pass.age;
      owner = "root";
      group = "root";
      mode = "0400";
      mounts = {
        containers = [ "web" ];
        ociContainers = [ "app" ];
      };
    };
  };
`
	if !strings.Contains(string(got), want) {
		t.Errorf("index.nix secrets section:\n%s\nwant to contain:\n%s", got, want)
	}
}

// TestSecretFileRoundTrip: ciphertext lands in secrets/<name>.age and is
// removed cleanly (removing a missing file is not an error — delete
// flows may race a never-applied secret).
func TestSecretFileRoundTrip(t *testing.T) {
	f := &StateFlake{Dir: t.TempDir()}
	if err := f.Init(); err != nil {
		t.Fatal(err)
	}
	if err := f.WriteSecret("db-pass", []byte("ciphertext")); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(f.Dir, "secrets", "db-pass.age"))
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "ciphertext" {
		t.Errorf("got %q", b)
	}
	if err := f.RemoveSecret("db-pass"); err != nil {
		t.Fatal(err)
	}
	if err := f.RemoveSecret("db-pass"); err != nil {
		t.Errorf("removing a missing secret should be a no-op, got %v", err)
	}
	// Path safety: the shared name rule guards the filesystem.
	if err := f.WriteSecret("../evil", []byte("x")); err == nil {
		t.Error("expected invalid name to be rejected")
	}
}

// TestSecretFileErrors covers the failure paths of the ciphertext
// writes: a blocked secrets/ path and removal of a non-file.
func TestSecretFileErrors(t *testing.T) {
	dir := t.TempDir()
	f := &StateFlake{Dir: dir}
	// secrets is a FILE: MkdirAll (write) and ReadDir (prune) both fail.
	if err := os.WriteFile(filepath.Join(dir, "secrets"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.WriteSecret("a", []byte("ct")); err == nil {
		t.Error("expected mkdir error when secrets is a file")
	}
	if err := f.PruneSecrets(nil); err == nil {
		t.Error("expected readdir error when secrets is a file")
	}
	if err := os.Remove(filepath.Join(dir, "secrets")); err != nil {
		t.Fatal(err)
	}

	// A directory named <name>.age: os.Remove fails with a real error
	// (not IsNotExist), for both RemoveSecret and the prune sweep.
	if err := os.MkdirAll(filepath.Join(dir, "secrets", "a.age"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "secrets", "a.age", "f"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := f.RemoveSecret("a"); err == nil {
		t.Error("expected remove error for non-empty directory")
	}
	if err := f.RemoveSecret("../evil"); err == nil {
		t.Error("expected invalid name to be rejected")
	}

	// Unwritable secrets dir: the atomic write cannot create its temp file.
	if err := os.Chmod(filepath.Join(dir, "secrets"), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(dir, "secrets"), 0o755) })
	if err := f.WriteSecret("b", []byte("ct")); err == nil {
		t.Error("expected write error in read-only dir")
	}
}

func TestPruneSecrets(t *testing.T) {
	dir := t.TempDir()
	f := &StateFlake{Dir: dir}

	// Pruning a flake that never had a secrets dir is a no-op.
	if err := f.PruneSecrets(nil); err != nil {
		t.Fatalf("prune without dir: %v", err)
	}

	if err := f.Init(); err != nil {
		t.Fatal(err)
	}
	for _, n := range []string{"keep", "orphan"} {
		if err := f.WriteSecret(n, []byte("ct")); err != nil {
			t.Fatal(err)
		}
	}
	// Non-.age entries are never touched: a stray file and a directory.
	if err := os.WriteFile(filepath.Join(dir, "secrets", "README"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "secrets", "sub.age"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := f.PruneSecrets([]string{"keep"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "secrets", "keep.age")); err != nil {
		t.Errorf("kept secret pruned: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "secrets", "orphan.age")); !os.IsNotExist(err) {
		t.Errorf("orphan survived prune: %v", err)
	}
	for _, n := range []string{"README", "sub.age"} {
		if _, err := os.Stat(filepath.Join(dir, "secrets", n)); err != nil {
			t.Errorf("non-secret entry %s touched: %v", n, err)
		}
	}

	// An orphan in a read-only dir makes the sweep's os.Remove fail.
	if err := f.WriteSecret("orphan2", []byte("ct")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, "secrets"), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Join(dir, "secrets"), 0o755) })
	if err := f.PruneSecrets([]string{"keep"}); err == nil {
		t.Error("expected prune error on undeletable orphan")
	}
}

// TestModulesDefaultConditionalSecrets: modules/default.nix imports
// secrets.nix behind the index-derived condition, so a secretless system
// never evaluates the age options.
func TestModulesDefaultConditionalSecrets(t *testing.T) {
	got := modulesDefault()
	if !strings.Contains(got, `++ (if ((import ../index.nix).secrets or { }) == { } then [ ] else [ ./secrets.nix ])`) {
		t.Errorf("modules/default.nix missing conditional secrets import:\n%s", got)
	}
}
