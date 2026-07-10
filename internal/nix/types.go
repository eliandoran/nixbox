package nix

import "fmt"

// WorkloadType is the single source of truth for everything that varies
// between kinds of workload: how a workload is exposed to the system
// flake, which systemd unit backs it, how its logs are read, and which
// starting-point templates it offers. Adding a new kind (OCI container,
// microvm, …) is one Register call plus its static NixOS module — no call
// site branches on the type string.
//
// The Nix-facing fields (IndexKey, ModuleFile, Module, Templates) are
// consumed by the state flake in this package; the machine-facing funcs
// (UnitName, JournalArgs, DataDir) are consumed by the machine package,
// which imports nix for exactly this descriptor.
type WorkloadType struct {
	// ID is the value stored in workloads.type, e.g. "nixos-container".
	ID string
	// Label is the sidebar heading grouping workloads of this type.
	Label string

	// IndexKey is the attribute in the generated index.nix holding this
	// type's name→path map (e.g. "containers"). Its static module reads
	// index.<IndexKey>.
	IndexKey string
	// ModuleFile is the path, relative to the state flake root, of this
	// type's static NixOS module; Module is its content. Init writes it
	// and lists it in modules/default.nix.
	ModuleFile string
	Module     string

	// Templates are the starting-point expressions offered when creating
	// a workload of this type.
	Templates []Template

	// ValidateName enforces type-specific name constraints beyond the
	// filesystem/path safety that ValidateName already guarantees — e.g.
	// the 11-char nspawn interface limit for nixos-containers.
	ValidateName func(name string) error
	// NamePattern (HTML pattern attribute, unanchored), NameMaxLen, and
	// NameHint drive the ID field on the create form so its client-side
	// constraints and help text match ValidateName.
	NamePattern string
	NameMaxLen  int
	NameHint    string

	// SupportsInsideJournal reports whether the type has a distinct
	// in-workload journal (a nixos-container does, via machinectl -M; an
	// OCI container logs only to its host unit). Drives the "journal from
	// inside" toggle on the detail page.
	SupportsInsideJournal bool

	// UnitName maps a workload name to the systemd unit that backs it.
	UnitName func(name string) string
	// JournalArgs returns the journalctl selector for this workload.
	// inside selects the in-container journal where the type supports it
	// (nixos-container via machinectl -M); types without a distinct
	// in-workload journal ignore it.
	JournalArgs func(name string, inside bool) []string
	// DataDir is the on-disk state path deleted on destroy-with-data, or
	// "" when the type keeps no such directory.
	DataDir func(name string) string
}

var (
	registry      = map[string]WorkloadType{}
	registryOrder []string
)

// Register adds a workload type to the registry. It panics on a duplicate
// ID because the type set is fixed at init time — a collision is a
// programming error, not a runtime condition.
func Register(wt WorkloadType) {
	if _, ok := registry[wt.ID]; ok {
		panic("nix: duplicate workload type " + wt.ID)
	}
	registry[wt.ID] = wt
	registryOrder = append(registryOrder, wt.ID)
}

// Lookup returns the descriptor for a type ID. ok is false for a type
// that was never registered (e.g. a stale row after a downgrade); callers
// that must proceed anyway fall back to the container type.
func Lookup(id string) (WorkloadType, bool) {
	wt, ok := registry[id]
	return wt, ok
}

// RegisteredTypes returns all types in registration order, so generated
// output (index sections, module list) is stable across runs.
func RegisteredTypes() []WorkloadType {
	types := make([]WorkloadType, 0, len(registryOrder))
	for _, id := range registryOrder {
		types = append(types, registry[id])
	}
	return types
}

// nixosContainerModule maps this type's index section into NixOS
// containers.<name>. The firewall merge lives in the shared hostPorts
// module, so this stays purely about the container set. `or { }` keeps it
// evaluating against an index generated before this type existed.
const nixosContainerModule = `{ lib, ... }:

let
  index = import ../index.nix;
in
{
  containers = lib.mapAttrs (name: path: import path) (index.containers or { });
}
`

// ociContainerModule maps this type's index section into
// virtualisation.oci-containers.containers.<name>. nixbox pins the podman
// backend so container logs land in the host journal
// (journalctl -u podman-<name>), matching how every other workload's logs
// are streamed; mkDefault lets the host override it. The upstream module
// contributes nothing when containers is empty, so podman is only pulled
// in once an OCI workload exists. `or { }` keeps it evaluating against an
// index generated before this type existed.
const ociContainerModule = `{ lib, ... }:

let
  index = import ../index.nix;
in
{
  virtualisation.oci-containers.backend = lib.mkDefault "podman";
  virtualisation.oci-containers.containers =
    lib.mapAttrs (name: path: import path) (index.ociContainers or { });
}
`

// validateContainerName layers the nixos-container-specific 11-char nspawn
// interface limit on top of the shared path-safety check.
func validateContainerName(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if len(name) > 11 {
		return fmt.Errorf("invalid container name %q: nixos-containers are limited to 11 characters (systemd-nspawn ve-<name> interface limit)", name)
	}
	return nil
}

func init() {
	Register(WorkloadType{
		ID:                    WorkloadTypeContainer,
		Label:                 "NixOS containers",
		IndexKey:              "containers",
		ModuleFile:            "modules/nixos-container.nix",
		Module:                nixosContainerModule,
		Templates:             containerTemplates,
		ValidateName:          validateContainerName,
		NamePattern:           `[a-z0-9]([a-z0-9-]{0,9}[a-z0-9])?`,
		NameMaxLen:            11,
		NameHint:              "1–11 characters of a-z 0-9 - (systemd-nspawn network interface limit). Used in URLs, on disk, and as the container's systemd identity — can't be changed later.",
		SupportsInsideJournal: true,
		UnitName:              func(name string) string { return "container@" + name + ".service" },
		JournalArgs: func(name string, inside bool) []string {
			if inside {
				// machinectl -M reads the journal from within the
				// running container.
				return []string{"-M", name}
			}
			return []string{"-u", "container@" + name + ".service"}
		},
		DataDir: func(name string) string { return "/var/lib/nixos-containers/" + name },
	})

	Register(WorkloadType{
		ID:           WorkloadTypeOCI,
		Label:        "OCI containers",
		IndexKey:     "ociContainers",
		ModuleFile:   "modules/oci-container.nix",
		Module:       ociContainerModule,
		Templates:    ociTemplates,
		ValidateName: ValidateName, // shared rule (1-63) is the OCI rule
		NamePattern:  `[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?`,
		NameMaxLen:   63,
		NameHint:     "1–63 characters of a-z 0-9 -. Used in URLs, on disk, and as the podman container and unit name — can't be changed later.",
		// An OCI container runs one process and logs to its host unit; it
		// has no in-container journald to read.
		SupportsInsideJournal: false,
		UnitName:              func(name string) string { return "podman-" + name + ".service" },
		JournalArgs: func(name string, _ bool) []string {
			return []string{"-u", "podman-" + name + ".service"}
		},
		// podman keeps container storage in its own graph/volumes, not a
		// per-workload directory nixbox can safely rm -rf; destroy leaves
		// it to the backend.
		DataDir: func(string) string { return "" },
	})
}
