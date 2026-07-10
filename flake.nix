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
      });

      devShells = forAllSystems (pkgs: {
        default = pkgs.mkShell {
          packages = with pkgs; [
            go
            gopls
            golangci-lint
            sqlite
          ];
        };
      });

      nixosModules.nixbox = { pkgs, ... }: {
        imports = [ ./nix/module.nix ];
        services.nixbox.package =
          nixpkgs.lib.mkDefault self.packages.${pkgs.stdenv.hostPlatform.system}.nixbox;
      };
    };
}
