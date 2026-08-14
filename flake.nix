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
        module = import ./nix/go.nix { inherit pkgs; };
      in
      {
        devShells.default = module.devShell;
        packages.dockerImage = module.dockerImage;
      });
}
