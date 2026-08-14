package main

import (
	"runtime/debug"
	"strings"
)

// version is injected at build time with -ldflags "-X main.version=...".
// Nix sets it from the package version; a plain `go build` leaves it empty and
// buildVersion falls back to the embedded build metadata.
var version string

// devVersion is reported when neither a linker flag nor build metadata is
// available, which in practice means `go run` from a source checkout.
const devVersion = "dev"

// buildVersion reports the binary's version string for --version.
//
// Precedence is linker flag, then module version, then VCS revision from the
// embedded build info, then "dev". It never returns an empty string, because
// an empty --version output is indistinguishable from a crash.
func buildVersion() string {
	if v := strings.TrimSpace(version); v != "" {
		return v
	}

	info, ok := debug.ReadBuildInfo()
	if !ok {
		return devVersion
	}

	if v := strings.TrimSpace(info.Main.Version); v != "" && v != "(devel)" {
		return v
	}

	if rev := vcsRevision(info); rev != "" {
		return devVersion + "+" + rev
	}

	return devVersion
}

// vcsRevision extracts a short commit hash from the embedded build settings,
// returning "" when the binary was not built from a VCS checkout.
func vcsRevision(info *debug.BuildInfo) string {
	const shortLen = 12

	for _, setting := range info.Settings {
		if setting.Key != "vcs.revision" {
			continue
		}
		rev := setting.Value
		if len(rev) > shortLen {
			rev = rev[:shortLen]
		}
		return rev
	}

	return ""
}
