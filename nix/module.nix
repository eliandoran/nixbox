{ config, lib, pkgs, ... }:

let
  cfg = config.services.nixbox;
in
{
  options.services.nixbox = {
    enable = lib.mkEnableOption "nixbox, a web interface for managing this NixOS server";

    package = lib.mkOption {
      type = lib.types.package;
      description = "The nixbox package to run.";
    };

    listenAddress = lib.mkOption {
      type = lib.types.str;
      default = "127.0.0.1:8368";
      description = "Address the web interface listens on.";
    };

    hostFlake = lib.mkOption {
      type = lib.types.str;
      default = "/etc/nixos";
      description = "Path to the flake that defines this system.";
    };

    hostAttr = lib.mkOption {
      type = lib.types.str;
      default = config.networking.hostName;
      defaultText = lib.literalExpression "config.networking.hostName";
      description = "Name of the nixosConfigurations attribute to rebuild.";
    };

    openFirewall = lib.mkOption {
      type = lib.types.bool;
      default = false;
      description = "Open the firewall for the listen port. Only relevant when listening on a non-loopback address.";
    };

    auth = lib.mkOption {
      type = lib.types.enum [ "pam" "none" ];
      default = "pam";
      description = ''
        Login backend. "pam" authenticates real system users against the
        generated nixbox PAM service (password logins — a key-only admin
        account needs users.users.<name>.hashedPassword set to sign in).
        "none" serves the interface unauthenticated: only for trusted
        loopback use or behind a reverse proxy that does its own auth.
      '';
    };

    allowedGroups = lib.mkOption {
      type = lib.types.listOf lib.types.str;
      default = [ "wheel" ];
      description = ''
        Unix groups whose members may log in when auth = "pam". A valid
        password alone is deliberately not enough — nixbox is
        root-equivalent, so an ordinary account must not qualify. root
        itself is always allowed.
      '';
    };
  };

  config = lib.mkIf cfg.enable {
    systemd.services.nixbox = {
      description = "nixbox web interface";
      wantedBy = [ "multi-user.target" ];
      after = [ "network.target" ];

      # nixos-rebuild re-execs tools from the system closure; give the
      # service the same PATH an interactive root shell would have.
      path = [
        pkgs.nix
        pkgs.nixos-rebuild
        pkgs.systemd
        pkgs.git
        pkgs.openssh
        config.nix.package
      ];

      environment = {
        # nix needs a writable cache/state home when run from a unit.
        HOME = "/root";
        NIXBOX_LISTEN = cfg.listenAddress;
        NIXBOX_HOST_FLAKE = cfg.hostFlake;
        NIXBOX_HOST_ATTR = cfg.hostAttr;
        NIXBOX_STATE_DIR = "/var/lib/nixbox";
        NIXBOX_AUTH = cfg.auth;
        NIXBOX_ALLOWED_GROUPS = lib.concatStringsSep "," cfg.allowedGroups;
      };

      serviceConfig = {
        ExecStart = "${lib.getExe cfg.package} serve";
        StateDirectory = "nixbox";
        StateDirectoryMode = "0755";
        Restart = "on-failure";
        RestartSec = 5;

        # Runs as root by necessity (nixos-rebuild, machinectl). Harden
        # what we can without breaking rebuilds or host flakes in /home.
        LockPersonality = true;
        RestrictSUIDSGID = true;
        ProtectHostname = true;
        ProtectKernelTunables = true;
        RestrictRealtime = true;
      };
    };

    networking.firewall.allowedTCPPorts = lib.mkIf cfg.openFirewall [
      (lib.toInt (lib.last (lib.splitString ":" cfg.listenAddress)))
    ];

    # An empty service definition yields NixOS's standard pam_unix stack
    # (auth/account/password/session) at /etc/pam.d/nixbox — the same
    # mechanism Cockpit uses. nixbox runs as root, so no shadow tricks.
    security.pam.services.nixbox = lib.mkIf (cfg.auth == "pam") { };

    warnings = lib.optional (cfg.auth == "none" && cfg.openFirewall)
      "services.nixbox: auth = \"none\" with openFirewall = true — anyone who can reach ${cfg.listenAddress} controls this machine.";
  };
}
