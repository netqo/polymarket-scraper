# Project build outputs, kept separate from nix/go.nix (which is the shared
# dev-setup toolchain module and stays verbatim).
#
# "Nix builds, Docker runs": the binary is built by buildGoModule from the same
# pinned nixpkgs as the devShell, and the image contains only that binary plus
# the CA bundle it needs to reach Polymarket over TLS. There is no Dockerfile.
{ pkgs, version ? "0.1.0" }:
let
  scraper = pkgs.buildGoModule {
    pname = "polymarket-scraper";
    inherit version;

    # cleanSource keeps .git and editor droppings out of the store path, so the
    # build is reproducible and does not rebuild on every commit.
    src = pkgs.lib.cleanSource ../.;

    # No external module dependencies yet. When the first one lands in go.mod,
    # replace this with the vendor hash Nix prints on the first failing build.
    vendorHash = null;

    subPackages = [ "cmd/polymarket-scraper" ];

    # main.version is the --version fallback when the binary is built by Nix
    # rather than by `go build` from a git checkout.
    ldflags = [
      "-s"
      "-w"
      "-X main.version=${version}"
    ];
  };
in
{
  package = scraper;

  dockerImage = pkgs.dockerTools.buildImage {
    name = "polymarket-scraper";
    tag = version;

    copyToRoot = pkgs.buildEnv {
      name = "polymarket-scraper-root";
      # cacert only: the scraper is read-only and credential-free, so it needs
      # no shell, no package manager, and no writable state.
      paths = [
        scraper
        pkgs.cacert
      ];
      pathsToLink = [
        "/bin"
        "/etc"
      ];
    };

    config = {
      Entrypoint = [ "/bin/polymarket-scraper" ];
      Env = [ "SSL_CERT_FILE=/etc/ssl/certs/ca-bundle.crt" ];
    };
  };
}
