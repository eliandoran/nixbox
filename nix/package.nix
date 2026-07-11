{ lib, buildGoModule }:

buildGoModule {
  pname = "nixbox";
  version = "0.1.0";

  src = lib.cleanSourceWith {
    src = ../.;
    filter = path: type:
      let rel = lib.removePrefix (toString ../. + "/") (toString path);
      in !(lib.hasPrefix "nix/" rel || lib.hasPrefix "dev-vm/" rel || rel == "flake.nix"
           # web/editor is the CodeMirror bundle *source* (npm); only its
           # committed build product web/static/codemirror.js is embedded.
           || lib.hasPrefix "web/editor/" rel);
  };

  vendorHash = "sha256-gwY10YMMUCLGen8jfcqBzZmlEcTz2YJepIQotHVwHF8=";

  subPackages = [ "cmd/nixbox" ];

  ldflags = [ "-s" "-w" ];

  meta = {
    description = "Web interface for managing a NixOS server's declarative containers";
    mainProgram = "nixbox";
    license = lib.licenses.mit;
    platforms = lib.platforms.linux;
  };
}
