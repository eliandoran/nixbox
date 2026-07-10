{
  description = "nixbox — web interface for managing a NixOS server's declarative containers";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs = { self, nixpkgs }:
    let
      systems = [ "x86_64-linux" "aarch64-linux" ];
      forAllSystems = f: nixpkgs.lib.genAttrs systems (system:
        f nixpkgs.legacyPackages.${system});
    in
    {
      packages = forAllSystems (pkgs: rec {
        nixbox = pkgs.callPackage ./nix/package.nix { };
        default = nixbox;
      } // nixpkgs.lib.optionalAttrs (pkgs.stdenv.hostPlatform.system == "x86_64-linux") {
        # Disposable dev VM: `nix build .#vm && ./result/bin/run-testhost-vm`
        vm = self.nixosConfigurations.devvm.config.system.build.vm;
      });

      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          packages = with pkgs; [
            go
            gopls
            golangci-lint
            sqlite
            # For regenerating web/static/codemirror.js (see web/editor).
            nodejs
            esbuild
          ];
        };
      });

      nixosModules.nixbox = { pkgs, ... }: {
        imports = [ ./nix/module.nix ];
        services.nixbox.package =
          nixpkgs.lib.mkDefault self.packages.${pkgs.stdenv.hostPlatform.system}.nixbox;
      };

      # Development VM: a throwaway NixOS machine running nixbox for
      # real (no dry-run), able to rebuild itself from its /etc/nixos.
      nixosConfigurations.devvm = nixpkgs.lib.nixosSystem {
        system = "x86_64-linux";
        specialArgs = { nixbox-src = self; };
        modules = [
          self.nixosModules.nixbox
          ./nix/dev-vm/configuration.nix
        ];
      };
    };
}
