# NixOS configuration for the nixbox development VM.
#
# This file is used twice: the host builds the VM from it via
# `nix build .#vm`, and the bootstrap service copies it into the
# guest's writable /etc/nixos (next to a generated flake.nix) so that
# nixbox inside the guest can run real `nixos-rebuild switch` against
# the same configuration. The generated flake's inputs are /nix/store
# paths, which the guest sees through the shared host store — so
# in-guest rebuilds need no network for sources.
{ config, pkgs, lib, nixbox-src, ... }:

let
  guestFlake = pkgs.writeText "flake.nix" ''
    {
      description = "nixbox dev VM system flake";

      inputs = {
        nixpkgs.url = "path:${pkgs.path}";
        nixbox.url = "path:${nixbox-src}";
        nixbox-state.url = "path:/var/lib/nixbox/state";
      };

      outputs = { self, nixpkgs, nixbox, nixbox-state }: {
        nixosConfigurations.testhost = nixpkgs.lib.nixosSystem {
          system = "${pkgs.stdenv.hostPlatform.system}";
          specialArgs = { nixbox-src = nixbox; };
          modules = [
            ./configuration.nix
            # The rebuilt system must keep the VM's 9p store mounts,
            # or switch-to-configuration tries to stop them mid-switch.
            "''${nixpkgs}/nixos/modules/virtualisation/qemu-vm.nix"
            nixbox.nixosModules.nixbox
            nixbox-state.nixosModules.default
          ];
        };
      };
    }
  '';
in
{
  networking.hostName = "testhost";

  # The VM boots directly from the kernel; in-guest `nixos-rebuild
  # switch` must not try to install a bootloader.
  boot.loader.grub.enable = false;
  fileSystems."/" = lib.mkDefault { device = "/dev/vda"; fsType = "ext4"; };

  nix.settings.experimental-features = [ "nix-command" "flakes" ];
  # Sandboxed builds need kernel namespace setups that don't work on
  # the VM's overlayed 9p store; dev VM only, so build unsandboxed.
  nix.settings.sandbox = false;

  # Containers without privateNetwork share the VM's network namespace,
  # so ports they serve must be opened in the VM's own firewall.
  networking.firewall.allowedTCPPorts = [ 8080 ];

  services.nixbox = {
    enable = true;
    listenAddress = "0.0.0.0:8368";
    openFirewall = true;
    hostFlake = "/etc/nixos";
    hostAttr = "testhost";
  };

  # Seed the guest's writable /etc/nixos and the nixbox state flake
  # before nixbox starts, so the first Apply in the UI just works.
  systemd.services.dev-vm-bootstrap = {
    description = "Seed /etc/nixos for the nixbox dev VM";
    wantedBy = [ "multi-user.target" ];
    before = [ "nixbox.service" ];
    requiredBy = [ "nixbox.service" ];
    path = [ pkgs.nix ];
    environment.HOME = "/root";
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
    };
    script = ''
      mkdir -p /etc/nixos
      if [ ! -f /etc/nixos/flake.nix ]; then
        cp ${guestFlake} /etc/nixos/flake.nix
        cp ${./configuration.nix} /etc/nixos/configuration.nix
        chmod 644 /etc/nixos/flake.nix /etc/nixos/configuration.nix
      fi
      NIXBOX_STATE_DIR=/var/lib/nixbox ${lib.getExe config.services.nixbox.package} init
      if [ ! -f /etc/nixos/flake.lock ]; then
        nix flake lock /etc/nixos
      fi
    '';
  };

  # Root console auto-login for poking around; the web UI is the
  # primary interface.
  services.getty.autologinUser = "root";
  users.users.root.initialPassword = "nixbox";

  virtualisation.vmVariant.virtualisation = {
    memorySize = 4096;
    cores = 4;
    diskSize = 8192;
    # Host port 18368 so a dry-run dev server on 8368 can keep running.
    # 18080 → 8080 exposes the nginx template container for testing.
    forwardPorts = [
      { from = "host"; host.port = 18368; guest.port = 8368; }
      { from = "host"; host.port = 18080; guest.port = 8080; }
    ];
  };

  system.stateVersion = "26.05";
}
