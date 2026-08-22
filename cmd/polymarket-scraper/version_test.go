// Test data: Invented data. Build metadata comes from the toolchain, not the exchange.

package main

import (
	"runtime/debug"
	"strings"
	"testing"
)

func TestBuildVersionPrefersLinkerFlag(t *testing.T) {
	t.Cleanup(func() { version = "" })
	version = "1.2.3"

	if got := buildVersion(); got != "1.2.3" {
		t.Fatalf("buildVersion() = %q, want %q", got, "1.2.3")
	}
}

func TestBuildVersionTrimsLinkerFlag(t *testing.T) {
	t.Cleanup(func() { version = "" })
	version = "  1.2.3\n"

	if got := buildVersion(); got != "1.2.3" {
		t.Fatalf("buildVersion() = %q, want %q", got, "1.2.3")
	}
}

// An empty --version output is indistinguishable from a crash, so the fallback
// chain must never produce one regardless of how the binary was built.
func TestBuildVersionNeverEmpty(t *testing.T) {
	t.Cleanup(func() { version = "" })
	version = "   "

	if got := strings.TrimSpace(buildVersion()); got == "" {
		t.Fatal("buildVersion() returned an empty string")
	}
}

func TestVCSRevisionShortensAndReportsAbsence(t *testing.T) {
	tests := []struct {
		name     string
		settings []debug.BuildSetting
		want     string
	}{
		{
			name:     "no vcs information",
			settings: []debug.BuildSetting{{Key: "GOARCH", Value: "amd64"}},
			want:     "",
		},
		{
			name:     "long revision is shortened",
			settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "0123456789abcdef0123"}},
			want:     "0123456789ab",
		},
		{
			name:     "short revision is passed through",
			settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "0123456"}},
			want:     "0123456",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := vcsRevision(&debug.BuildInfo{Settings: tt.settings})
			if got != tt.want {
				t.Fatalf("vcsRevision() = %q, want %q", got, tt.want)
			}
		})
	}
}
