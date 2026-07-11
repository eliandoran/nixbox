package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFromEnvDefaults(t *testing.T) {
	// t.Setenv with "" reads as unset through envOr, and restores any
	// real value after the test.
	for _, k := range []string{
		"NIXBOX_LISTEN", "NIXBOX_STATE_DIR", "NIXBOX_HOST_FLAKE", "NIXBOX_HOST_ATTR",
		"NIXBOX_AGE_RECIPIENT", "NIXBOX_DRY_RUN", "NIXBOX_TERMINAL", "NIXBOX_DEV", "NIXBOX_LANG",
	} {
		t.Setenv(k, "")
	}
	cfg := FromEnv()
	if cfg.Listen != "127.0.0.1:8368" || cfg.StateDir != "./dev-state" ||
		cfg.HostFlake != "/etc/nixos" || cfg.AgeRecipient != "/etc/ssh/ssh_host_ed25519_key.pub" ||
		cfg.Lang != "en" {
		t.Errorf("defaults: %+v", cfg)
	}
	if cfg.DryRun || cfg.EnableTerminal || cfg.Dev {
		t.Errorf("boolean flags default on: %+v", cfg)
	}
	// HostAttr defaults to the machine's hostname.
	if h, err := os.Hostname(); err == nil && cfg.HostAttr != h {
		t.Errorf("HostAttr = %q, want hostname %q", cfg.HostAttr, h)
	}
}

func TestFromEnvOverrides(t *testing.T) {
	t.Setenv("NIXBOX_LISTEN", "0.0.0.0:1234")
	t.Setenv("NIXBOX_STATE_DIR", "/var/lib/nixbox")
	t.Setenv("NIXBOX_HOST_FLAKE", "/srv/flake")
	t.Setenv("NIXBOX_HOST_ATTR", "myhost")
	t.Setenv("NIXBOX_AGE_RECIPIENT", "/keys/k.pub")
	t.Setenv("NIXBOX_DRY_RUN", "1")
	t.Setenv("NIXBOX_TERMINAL", "1")
	t.Setenv("NIXBOX_DEV", "1")
	t.Setenv("NIXBOX_LANG", "ro")

	cfg := FromEnv()
	want := Config{
		Listen: "0.0.0.0:1234", StateDir: "/var/lib/nixbox", HostFlake: "/srv/flake",
		HostAttr: "myhost", AgeRecipient: "/keys/k.pub",
		DryRun: true, EnableTerminal: true, Dev: true, Lang: "ro",
	}
	if cfg != want {
		t.Errorf("cfg = %+v, want %+v", cfg, want)
	}
}

func TestDerivedPaths(t *testing.T) {
	c := Config{StateDir: "/var/lib/nixbox"}
	for got, want := range map[string]string{
		c.StateFlakeDir(): "/var/lib/nixbox/state",
		c.WorkloadsDir():  "/var/lib/nixbox/state/workloads",
		c.LogsDir():       "/var/lib/nixbox/logs",
		c.DBPath():        "/var/lib/nixbox/nixbox.db",
	} {
		if got != filepath.Clean(want) {
			t.Errorf("path = %q, want %q", got, want)
		}
	}
}
