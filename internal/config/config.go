// Package config holds nixbox's runtime configuration, resolved from
// environment variables (set by the NixOS module) with sane dev defaults.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Auth backends. PAM asks the host's PAM stack (the "nixbox" service) to
// verify a Unix user's password and is the production default; None serves
// everything unauthenticated, for dry-run dev servers and setups that
// terminate auth at a reverse proxy.
const (
	AuthPAM  = "pam"
	AuthNone = "none"
)

type Config struct {
	// Listen is the address the HTTP server binds to.
	Listen string
	// StateDir is the root of nixbox's persistent state
	// (database, job logs, and the managed state flake).
	StateDir string
	// HostFlake is the path to the flake defining this system.
	HostFlake string
	// HostAttr is the nixosConfigurations attribute to rebuild.
	HostAttr string
	// AgeRecipient is the SSH public key file secrets are encrypted to.
	// The default is the host's ed25519 host key — the identity agenix
	// decrypts with by default at activation — so on a normal NixOS host
	// running sshd nothing needs configuring. Overridable for dev setups
	// without a host key.
	AgeRecipient string
	// DryRun makes command runners log instead of execute, and
	// downgrades `nixos-rebuild switch` to `build`.
	DryRun bool
	// EnableTerminal exposes the interactive host/workload shells (the web
	// terminal). Off by default and orthogonal to DryRun: dry-run governs
	// whether nixbox mutates the system, whereas a live shell is arbitrary
	// user execution that dry-run cannot neuter, so it stays behind its own
	// explicit opt-in. It is gated by the login like every route, but a
	// root console is a bigger grant than buttons — hence the extra switch.
	EnableTerminal bool
	// Dev serves static assets and templates live from ./web on disk
	// (and re-parses templates per request) so JS/CSS/HTML edits show
	// on a browser refresh without recompiling. Off in production,
	// where everything is served from the embedded FS.
	Dev bool
	// Lang is the default UI locale, used when a request expresses no
	// preference (no cookie, no matching Accept-Language). Falls back to
	// English if the catalog is missing.
	Lang string
	// Auth selects the login backend (AuthPAM or AuthNone). The default
	// is PAM — a bare binary fails closed rather than serving a root-
	// equivalent UI unauthenticated; dev setups opt out explicitly.
	Auth string
	// AllowedGroups lists the Unix groups whose members may log in when
	// Auth is PAM. Authentication alone is deliberately not enough: any
	// PAM-valid user would otherwise control the machine. root is always
	// allowed.
	AllowedGroups []string
}

func FromEnv() (Config, error) {
	cfg := Config{
		Listen:         envOr("NIXBOX_LISTEN", "127.0.0.1:8368"),
		StateDir:       envOr("NIXBOX_STATE_DIR", "./dev-state"),
		HostFlake:      envOr("NIXBOX_HOST_FLAKE", "/etc/nixos"),
		HostAttr:       envOr("NIXBOX_HOST_ATTR", hostname()),
		AgeRecipient:   envOr("NIXBOX_AGE_RECIPIENT", "/etc/ssh/ssh_host_ed25519_key.pub"),
		DryRun:         os.Getenv("NIXBOX_DRY_RUN") != "",
		EnableTerminal: os.Getenv("NIXBOX_TERMINAL") != "",
		Dev:            os.Getenv("NIXBOX_DEV") != "",
		Lang:           envOr("NIXBOX_LANG", "en"),
		Auth:           envOr("NIXBOX_AUTH", AuthPAM),
		AllowedGroups:  splitGroups(envOr("NIXBOX_ALLOWED_GROUPS", "wheel")),
	}
	if cfg.Auth != AuthPAM && cfg.Auth != AuthNone {
		return Config{}, fmt.Errorf("NIXBOX_AUTH=%q: must be %q or %q", cfg.Auth, AuthPAM, AuthNone)
	}
	return cfg, nil
}

// splitGroups parses the comma-separated NIXBOX_ALLOWED_GROUPS value,
// ignoring blanks so trailing commas and spacing don't matter.
func splitGroups(v string) []string {
	var out []string
	for g := range strings.SplitSeq(v, ",") {
		if g = strings.TrimSpace(g); g != "" {
			out = append(out, g)
		}
	}
	return out
}

// StateFlakeDir is the directory holding the nixbox-managed flake
// (the exportable unit referenced by the host flake as a path input).
func (c Config) StateFlakeDir() string { return filepath.Join(c.StateDir, "state") }

// WorkloadsDir holds one directory per workload inside the state flake.
func (c Config) WorkloadsDir() string { return filepath.Join(c.StateFlakeDir(), "workloads") }

// LogsDir holds one log file per job.
func (c Config) LogsDir() string { return filepath.Join(c.StateDir, "logs") }

// DBPath is the SQLite database location.
func (c Config) DBPath() string { return filepath.Join(c.StateDir, "nixbox.db") }

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "default"
	}
	return h
}
