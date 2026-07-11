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

// TestModulesDefaultConditionalSecrets: modules/default.nix imports
// secrets.nix behind the index-derived condition, so a secretless system
// never evaluates the age options.
func TestModulesDefaultConditionalSecrets(t *testing.T) {
	got := modulesDefault()
	if !strings.Contains(got, `++ (if ((import ../index.nix).secrets or { }) == { } then [ ] else [ ./secrets.nix ])`) {
		t.Errorf("modules/default.nix missing conditional secrets import:\n%s", got)
	}
}
