package nix

import (
	"os"
	"path/filepath"
	"strings"
)

// Secrets in the state flake
//
// A secret is age ciphertext on disk (secrets/<name>.age) plus an entry
// in the generated index.nix carrying the decrypted file's identity and
// which workloads the secret is delivered into. The static
// modules/secrets.nix maps the index entries onto age.secrets.<name>,
// and each workload type's module delivers mounted secrets into its
// workloads at the fixed path /run/agenix/<name> — the same string
// whether the workload is a nixos-container (bind mount) or an OCI
// container (podman volume). Host services read the host path directly.
//
// The agenix module that declares the age.* options is imported by the
// generated flake output exactly when secrets exist (see renderFlake);
// modules/default.nix imports secrets.nix under the same index-derived
// condition, so a secretless system never evaluates either.

// secretsModule is the static module declaring every nixbox-managed
// secret to agenix. Ciphertext paths, ownership, and mode come from the
// index; agenix decrypts each file during activation using the host's
// SSH host key as its identity (its age.identityPaths default).
const secretsModule = `{ lib, ... }:

let
  index = import ../index.nix;
in
{
  # One agenix declaration per nixbox-managed secret. The decrypted file
  # lands at config.age.secrets.<name>.path (/run/agenix/<name>).
  age.secrets = lib.mapAttrs
    (name: s: { inherit (s) file owner group mode; })
    (index.secrets or { });
}
`

// IndexSecret is one secret to expose in the generated index: its
// ciphertext file is implied by the name (./secrets/<Name>.age), and
// Mounts lists the enabled workloads it is delivered into, keyed by the
// workload type's IndexKey.
type IndexSecret struct {
	Name  string
	Owner string // owner of the decrypted file on the host
	Group string
	Mode  string
	Mounts map[string][]string // IndexKey → workload names
}

// WriteSecret writes a secret's age ciphertext to its slot in the state
// flake. Ciphertext only — the file is world-readable by design, both
// here and once copied into the Nix store.
func (f *StateFlake) WriteSecret(name string, ciphertext []byte) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	dir := filepath.Join(f.Dir, "secrets")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(dir, name+".age"), ciphertext)
}

// RemoveSecret deletes a secret's ciphertext. The caller must regenerate
// the index first so the flake never references a missing file.
func (f *StateFlake) RemoveSecret(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	err := os.Remove(filepath.Join(f.Dir, "secrets", name+".age"))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// PruneSecrets removes ciphertext files whose secret no longer exists.
// Deleting a secret only drops its row (the on-disk index may still
// reference the file until the next apply); the apply path calls this
// right after regenerating the index, when nothing references the
// orphans anymore.
func (f *StateFlake) PruneSecrets(keep []string) error {
	keepSet := make(map[string]bool, len(keep))
	for _, n := range keep {
		keepSet[n+".age"] = true
	}
	entries, err := os.ReadDir(filepath.Join(f.Dir, "secrets"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".age") && !keepSet[e.Name()] {
			if err := os.Remove(filepath.Join(f.Dir, "secrets", e.Name())); err != nil {
				return err
			}
		}
	}
	return nil
}
