// Package config holds nixbox's runtime configuration, resolved from
// environment variables (set by the NixOS module) with sane dev defaults.
package config

import (
	"os"
	"path/filepath"
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
	// DryRun makes command runners log instead of execute, and
	// downgrades `nixos-rebuild switch` to `build`.
	DryRun bool
	// Dev serves static assets and templates live from ./web on disk
	// (and re-parses templates per request) so JS/CSS/HTML edits show
	// on a browser refresh without recompiling. Off in production,
	// where everything is served from the embedded FS.
	Dev bool
	// Lang is the default UI locale, used when a request expresses no
	// preference (no cookie, no matching Accept-Language). Falls back to
	// English if the catalog is missing.
	Lang string
}

func FromEnv() Config {
	cfg := Config{
		Listen:    envOr("NIXBOX_LISTEN", "127.0.0.1:8368"),
		StateDir:  envOr("NIXBOX_STATE_DIR", "./dev-state"),
		HostFlake: envOr("NIXBOX_HOST_FLAKE", "/etc/nixos"),
		HostAttr:  envOr("NIXBOX_HOST_ATTR", hostname()),
		DryRun:    os.Getenv("NIXBOX_DRY_RUN") != "",
		Dev:       os.Getenv("NIXBOX_DEV") != "",
		Lang:      envOr("NIXBOX_LANG", "en"),
	}
	return cfg
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
