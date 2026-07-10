package nix

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"

	"github.com/elian/nixbox/internal/run"
)

// RebuildMode selects what nixos-rebuild does with the built system.
type RebuildMode string

const (
	// ModeSwitch activates the configuration and makes it the boot default.
	ModeSwitch RebuildMode = "switch"
	// ModeTest activates without adding a boot entry.
	ModeTest RebuildMode = "test"
	// ModeBuild only builds — used for validation dry-runs.
	ModeBuild RebuildMode = "build"
)

// Pipeline executes the apply sequence:
//
//  1. refresh the nixbox-state path input's lock entry so the host
//     flake sees the current state dir contents,
//  2. nixos-rebuild against the host flake.
//
// The caller is responsible for having written workload files and the
// index beforehand. nixos-rebuild builds the full system before
// activating anything, so a failure at any step leaves the running
// system untouched.
type Pipeline struct {
	Runner    run.Runner
	HostFlake string
	HostAttr  string
	// StateInputName is the host flake's input name for the state
	// flake ("nixbox-state" per the documented setup).
	StateInputName string
	// DryRun downgrades switch/test to build so development never
	// activates anything.
	DryRun bool
}

// Rebuild runs the pipeline, streaming all command output to log.
// It returns the command exit code and, on success, the current system
// generation.
func (p *Pipeline) Rebuild(ctx context.Context, log io.Writer, mode RebuildMode) (int, *int64, error) {
	fmt.Fprintf(log, "==> refreshing %s input\n", p.StateInputName)
	code, err := p.Runner.Stream(ctx, log, "nix", "flake", "update", p.StateInputName,
		"--flake", p.HostFlake)
	if err != nil {
		return -1, nil, fmt.Errorf("nix flake update: %w", err)
	}
	if code != 0 {
		return code, nil, nil
	}

	if p.DryRun && mode != ModeBuild {
		fmt.Fprintf(log, "==> dry-run: using 'nixos-rebuild build' instead of '%s'\n", mode)
		mode = ModeBuild
	}

	fmt.Fprintf(log, "==> nixos-rebuild %s\n", mode)
	code, err = p.Runner.Stream(ctx, log, "nixos-rebuild", string(mode),
		"--flake", fmt.Sprintf("%s#%s", p.HostFlake, p.HostAttr))
	if err != nil {
		return -1, nil, fmt.Errorf("nixos-rebuild: %w", err)
	}
	if code != 0 {
		return code, nil, nil
	}

	gen, err := CurrentGeneration()
	if err != nil {
		// Not fatal: dry-run and build modes don't move the profile.
		fmt.Fprintf(log, "note: could not determine system generation: %v\n", err)
		return 0, nil, nil
	}
	fmt.Fprintf(log, "==> done, system generation %d\n", gen)
	return 0, &gen, nil
}

var genLinkRe = regexp.MustCompile(`^system-(\d+)-link$`)

// CurrentGeneration reads the active system generation number from the
// system profile symlink.
func CurrentGeneration() (int64, error) {
	target, err := os.Readlink("/nix/var/nix/profiles/system")
	if err != nil {
		return 0, err
	}
	m := genLinkRe.FindStringSubmatch(filepath.Base(target))
	if m == nil {
		return 0, fmt.Errorf("unexpected system profile target %q", target)
	}
	return strconv.ParseInt(m[1], 10, 64)
}
