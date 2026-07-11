package nix

import (
	"context"
	"errors"
	"os/exec"
	"strings"
)

// Validation runs read-only nix commands directly (not through the
// job Runner): it is always safe, so it works identically in dry-run
// mode. If the nix tools are missing, checks pass with a note so the
// UI remains usable on non-Nix dev machines.

// CheckSyntax parses a workload file without evaluating it. Returns
// nil if the file parses; otherwise the parser error, cleaned up for
// display.
func CheckSyntax(ctx context.Context, path string) error {
	bin, err := exec.LookPath("nix-instantiate")
	if err != nil {
		return nil // no nix toolchain available; skip
	}
	out, err := exec.CommandContext(ctx, bin, "--parse", path).CombinedOutput()
	if err != nil {
		return errors.New(cleanNixError(string(out)))
	}
	return nil
}

// CheckEval imports the workload file and forces its top-level
// attribute names, catching evaluation errors beyond syntax (missing
// semicolons parse fine but undefined variables don't eval).
//
// A function-form workload is applied to stub arguments synthesized from
// its own pattern (builtins.functionArgs), which covers both shapes the
// composition accepts: the { flakeInputs }: input-consuming wrapper and a
// host-service written as an ordinary module function
// ({ config, pkgs, ... }: …). The stubs are empty attrsets — attrNames
// only forces the top-level keys, so references *through* a stub (e.g.
// flakeInputs.<name>, pkgs.foo) stay lazy and unevaluated; resolving the
// real values is the apply pipeline's job.
func CheckEval(ctx context.Context, path string) error {
	bin, err := exec.LookPath("nix")
	if err != nil {
		return nil
	}
	const apply = `v: builtins.attrNames (
	  if builtins.isFunction v
	  then v (builtins.mapAttrs (_: _: { }) (builtins.functionArgs v))
	  else v)`
	out, err := exec.CommandContext(ctx, bin, "eval", "--json",
		"--file", path, "--apply", apply).CombinedOutput()
	if err != nil {
		return errors.New(cleanNixError(string(out)))
	}
	return nil
}

// cleanNixError trims noise (trace lines, blank lines) from nix
// diagnostics so they fit an inline banner.
func cleanNixError(out string) string {
	var keep []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "at «") {
			continue
		}
		keep = append(keep, line)
		if len(keep) >= 8 {
			keep = append(keep, "…")
			break
		}
	}
	if len(keep) == 0 {
		return "nix reported an unknown error"
	}
	return strings.Join(keep, "\n")
}
