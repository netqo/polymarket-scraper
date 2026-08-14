{
  description = "polymarket-scraper dev environment (pinned via Nix; Nix builds, Docker runs)";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachSystem [ "x86_64-linux" "aarch64-linux" ] (system:
      let
        pkgs = import nixpkgs { inherit system; };
        toolchain = import ./nix/go.nix { inherit pkgs; };
        project = import ./nix/package.nix { inherit pkgs; };
      in
      {
        devShells.default = toolchain.devShell;

        packages.default = project.package;
        packages.polymarket-scraper = project.package;

        # Deliberately shadows toolchain.dockerImage: CI must build the artifact
        # we ship, not an image of the Go toolchain.
        packages.dockerImage = project.dockerImage;
      });
}
