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
        NIXBOX_LISTEN = cfg.listenAddress;
        NIXBOX_HOST_FLAKE = cfg.hostFlake;
        NIXBOX_HOST_ATTR = cfg.hostAttr;
        NIXBOX_STATE_DIR = "/var/lib/nixbox";
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
  };
}
