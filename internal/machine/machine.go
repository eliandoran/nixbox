// Package machine wraps systemctl/machinectl for querying and driving
// NixOS containers (systemd-nspawn units named container@<name>).
package machine

import (
	"context"
	"fmt"
	"os/exec"
	"strings"

	"github.com/elian/nixbox/internal/run"
)

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

func unit(name string) string { return "container@" + name + ".service" }

// Status queries systemd for a container's unit state. A container that
// was never applied reports inactive/dead.
func (m *Manager) Status(ctx context.Context, name string) (Status, error) {
	out, err := m.Runner.Output(ctx, "systemctl", "show", unit(name),
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

func (m *Manager) Start(ctx context.Context, name string) error {
	return m.verb(ctx, "start", name)
}

func (m *Manager) Stop(ctx context.Context, name string) error {
	return m.verb(ctx, "stop", name)
}

func (m *Manager) Restart(ctx context.Context, name string) error {
	return m.verb(ctx, "restart", name)
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
// container. inside switches from the host-side unit journal to the
// journal written within the container (requires it to be running).
// Reading journals is side-effect free, so this runs directly rather
// than through the (possibly dry-run) Runner.
func JournalCommand(ctx context.Context, name string, inside bool) *exec.Cmd {
	args := []string{"--follow", "--lines=200", "--no-pager", "--output=short-iso"}
	if inside {
		args = append(args, "-M", name)
	} else {
		args = append(args, "-u", unit(name))
	}
	return exec.CommandContext(ctx, "journalctl", args...)
}

func (m *Manager) verb(ctx context.Context, verb, name string) error {
	if _, err := m.Runner.Output(ctx, "systemctl", verb, unit(name)); err != nil {
		return fmt.Errorf("systemctl %s %s: %w", verb, unit(name), err)
	}
	return nil
}
