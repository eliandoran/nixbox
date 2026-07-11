{ lib, buildGoModule, esbuild }:

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

  # web/static/app.js is a build product (gitignored, so never part of
  # the flake source): bundle the TypeScript in web/src before the Go
  # build embeds it. Same invocation as `just bundle`.
  nativeBuildInputs = [ esbuild ];
  preBuild = ''
    esbuild web/src/main.ts --bundle --format=iife --outfile=web/static/app.js
  '';

  ldflags = [ "-s" "-w" ];

  meta = {
    description = "Web interface for managing a NixOS server's declarative containers";
    mainProgram = "nixbox";
    license = lib.licenses.mit;
    platforms = lib.platforms.linux;
  };
}
