// nixbox — a web interface for managing a NixOS server's declarative
// containers.
package main

import (
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/elian/nixbox/internal/config"
	"github.com/elian/nixbox/internal/jobs"
	"github.com/elian/nixbox/internal/machine"
	"github.com/elian/nixbox/internal/nix"
	"github.com/elian/nixbox/internal/run"
	"github.com/elian/nixbox/internal/server"
	"github.com/elian/nixbox/internal/store"
)

const usage = `usage: nixbox <command>

commands:
  serve   run the web interface (configured via NIXBOX_* env vars)
  init    seed the state flake under $NIXBOX_STATE_DIR without serving

environment:
  NIXBOX_LISTEN      listen address        (default 127.0.0.1:8368)
  NIXBOX_STATE_DIR   state directory       (default ./dev-state)
  NIXBOX_HOST_FLAKE  flake of this system  (default /etc/nixos)
  NIXBOX_HOST_ATTR   nixosConfigurations attribute (default: hostname)
  NIXBOX_AGE_RECIPIENT  SSH public key secrets are encrypted to
                     (default /etc/ssh/ssh_host_ed25519_key.pub)
  NIXBOX_DRY_RUN     if set, log commands instead of executing
  NIXBOX_AUTH        login backend: pam or none    (default pam)
  NIXBOX_ALLOWED_GROUPS  groups whose members may log in, comma-
                     separated; root always may    (default wheel)
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	cfg, err := config.FromEnv()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n\n%s", err, usage)
		os.Exit(2)
	}

	switch os.Args[1] {
	case "serve":
		err = serve(cfg)
	case "init":
		flake := &nix.StateFlake{Dir: cfg.StateFlakeDir()}
		if err = flake.Init(); err == nil {
			fmt.Printf("state flake initialized at %s\n", cfg.StateFlakeDir())
			fmt.Printf("add to your host flake inputs:\n\n")
			fmt.Printf("  nixbox-state.url = \"path:%s\";\n", cfg.StateFlakeDir())
		}
	case "help", "-h", "--help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func serve(cfg config.Config) error {
	slog.Info("starting nixbox",
		"listen", cfg.Listen,
		"stateDir", cfg.StateDir,
		"hostFlake", cfg.HostFlake,
		"hostAttr", cfg.HostAttr,
		"dryRun", cfg.DryRun,
		"auth", cfg.Auth)

	flake := &nix.StateFlake{Dir: cfg.StateFlakeDir()}
	if err := flake.Init(); err != nil {
		return fmt.Errorf("initializing state flake: %w", err)
	}

	st, err := store.Open(cfg.DBPath())
	if err != nil {
		return fmt.Errorf("opening database: %w", err)
	}
	defer st.Close()

	jm, err := jobs.NewManager(st, cfg.LogsDir())
	if err != nil {
		return err
	}
	if err := jm.RecoverStale(); err != nil {
		return err
	}

	var runner run.Runner = run.Exec{}
	if cfg.DryRun {
		runner = run.DryRun{}
	}

	pipeline := &nix.Pipeline{
		Runner:         runner,
		HostFlake:      cfg.HostFlake,
		HostAttr:       cfg.HostAttr,
		StateDir:       cfg.StateFlakeDir(),
		StateInputName: "nixbox-state",
		DryRun:         cfg.DryRun,
	}
	machines := &machine.Manager{Runner: runner}

	srv, err := server.New(cfg, st, flake, jm, pipeline, machines)
	if err != nil {
		return err
	}

	slog.Info("listening", "addr", "http://"+cfg.Listen)
	return http.ListenAndServe(cfg.Listen, srv.Handler())
}
