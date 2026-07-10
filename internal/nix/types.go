package nix

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

func init() {
	Register(WorkloadType{
		ID:           WorkloadTypeContainer,
		Label:        "NixOS containers",
		IndexKey:     "containers",
		ModuleFile:   "modules/nixos-container.nix",
		Module:       nixosContainerModule,
		Templates:    containerTemplates,
		ValidateName: ValidateName,
		UnitName:     func(name string) string { return "container@" + name + ".service" },
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
}
