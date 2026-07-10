// Package machine wraps systemctl/machinectl for querying and driving
// workloads. The systemd unit that backs a workload, and how its journal
// is read, are type-specific: every entry point takes the workload's
// nix.WorkloadType descriptor, which supplies the unit name and journal
// selector (e.g. container@<name>.service for a nixos-container).
package machine

import (
	"context"
	"fmt"
	"math"
	"os/exec"
	"strconv"
	"strings"

	"github.com/elian/nixbox/internal/nix"
	"github.com/elian/nixbox/internal/run"
)

// Ref names a workload together with its type, so the machine layer can
// resolve the backing systemd unit without a second lookup.
type Ref struct {
	Type nix.WorkloadType
	Name string
}

type Status struct {
	// ActiveState is systemd's high-level state: active, inactive,
	// failed, activating, deactivating.
	ActiveState string
	// SubState refines it (e.g. running, dead).
	SubState string
}

func (s Status) Running() bool { return s.ActiveState == "active" }

type Manager struct {
	Runner run.Runner
}

// Status queries systemd for a workload's unit state. A workload that was
// never applied reports inactive/dead.
func (m *Manager) Status(ctx context.Context, wt nix.WorkloadType, name string) (Status, error) {
	out, err := m.Runner.Output(ctx, "systemctl", "show", wt.UnitName(name),
		"--property=ActiveState,SubState")
	if err != nil {
		return Status{}, err
	}
	st := Status{ActiveState: "unknown", SubState: "unknown"}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch k {
		case "ActiveState":
			st.ActiveState = v
		case "SubState":
			st.SubState = v
		}
	}
	return st, nil
}

// Usage is a point-in-time resource snapshot for one container, read
// from systemd's own accounting. CPUNSec is cumulative CPU time since
// the unit started; callers derive a percentage by sampling twice and
// dividing the delta by the wall-clock elapsed. Fields are zero for a
// container that isn't running (systemd reports no accounting for it).
type Usage struct {
	Running  bool
	MemBytes uint64
	CPUNSec  uint64
	Tasks    uint64
}

// Usages returns resource snapshots for the given workloads in a single
// systemctl call. Workloads with no unit (never applied) are simply
// absent from the result. Like Status, this is a read-only query that
// still runs in dry-run mode — where the runner yields no output, so the
// map is empty. The result is keyed by workload name.
func (m *Manager) Usages(ctx context.Context, refs []Ref) (map[string]Usage, error) {
	if len(refs) == 0 {
		return map[string]Usage{}, nil
	}
	// Units may follow different naming schemes per type, so key results
	// back to names via the exact unit strings we asked about rather than
	// stripping a fixed prefix.
	unitToName := make(map[string]string, len(refs))
	args := []string{"show", "--property=Id,ActiveState,MemoryCurrent,CPUUsageNSec,TasksCurrent"}
	for _, ref := range refs {
		u := ref.Type.UnitName(ref.Name)
		unitToName[u] = ref.Name
		args = append(args, u)
	}
	out, err := m.Runner.Output(ctx, "systemctl", args...)
	if err != nil {
		return nil, err
	}
	return parseUsages(out, unitToName), nil
}

// parseUsages turns `systemctl show` output for multiple units into a map
// keyed by workload name. Units are emitted as blank-line-separated
// blocks; each self-identifies via its Id, which unitToName maps back to
// the workload name.
func parseUsages(out string, unitToName map[string]string) map[string]Usage {
	usages := map[string]Usage{}
	for _, block := range strings.Split(strings.TrimSpace(out), "\n\n") {
		var name string
		var u Usage
		for _, line := range strings.Split(block, "\n") {
			k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
			if !ok {
				continue
			}
			switch k {
			case "Id":
				name = unitToName[v]
			case "ActiveState":
				u.Running = v == "active"
			case "MemoryCurrent":
				u.MemBytes = parseAccounting(v)
			case "CPUUsageNSec":
				u.CPUNSec = parseAccounting(v)
			case "TasksCurrent":
				u.Tasks = parseAccounting(v)
			}
		}
		if name != "" {
			usages[name] = u
		}
	}
	return usages
}

// parseAccounting reads a systemd accounting counter, mapping its
// "unavailable" sentinels ([not set] and UINT64_MAX) to zero.
func parseAccounting(v string) uint64 {
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil || n == math.MaxUint64 {
		return 0
	}
	return n
}

func (m *Manager) Start(ctx context.Context, wt nix.WorkloadType, name string) error {
	return m.verb(ctx, "start", wt, name)
}

func (m *Manager) Stop(ctx context.Context, wt nix.WorkloadType, name string) error {
	return m.verb(ctx, "stop", wt, name)
}

func (m *Manager) Restart(ctx context.Context, wt nix.WorkloadType, name string) error {
	return m.verb(ctx, "restart", wt, name)
}

// Reboot restarts the host. systemctl queues the job and returns
// immediately, so the caller can still send a response before the
// machine goes down.
func (m *Manager) Reboot(ctx context.Context) error {
	if _, err := m.Runner.Output(ctx, "systemctl", "reboot"); err != nil {
		return fmt.Errorf("systemctl reboot: %w", err)
	}
	return nil
}

// Poweroff shuts the host down. Same fire-and-return behaviour as Reboot.
func (m *Manager) Poweroff(ctx context.Context) error {
	if _, err := m.Runner.Output(ctx, "systemctl", "poweroff"); err != nil {
		return fmt.Errorf("systemctl poweroff: %w", err)
	}
	return nil
}

// JournalCommand builds a follow-mode journalctl invocation for a
// workload. inside switches from the host-side unit journal to the
// journal written within the workload where the type supports it (a
// running nixos-container), requiring it to be running. Reading journals
// is side-effect free, so this runs directly rather than through the
// (possibly dry-run) Runner.
func JournalCommand(ctx context.Context, wt nix.WorkloadType, name string, inside bool) *exec.Cmd {
	args := []string{"--follow", "--lines=200", "--no-pager", "--output=short-iso"}
	args = append(args, wt.JournalArgs(name, inside)...)
	return exec.CommandContext(ctx, "journalctl", args...)
}

func (m *Manager) verb(ctx context.Context, verb string, wt nix.WorkloadType, name string) error {
	unit := wt.UnitName(name)
	if _, err := m.Runner.Output(ctx, "systemctl", verb, unit); err != nil {
		return fmt.Errorf("systemctl %s %s: %w", verb, unit, err)
	}
	return nil
}
