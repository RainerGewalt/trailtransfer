// Package version exposes the build version, injected at link time via
// -ldflags "-X github.com/RainerGewalt/trailtransfer/internal/version.Version=...".
// It appears in every capabilities and result event.
package version

import (
	"runtime/debug"
	"strings"
)

// Version is the worker version. Overridden at build time; "dev" otherwise.
var Version = "dev"

// String returns the current worker version without a leading "v". A binary
// built with `go install ...@v0.1.1` has no ldflags; it takes the module
// version from the build info, so its records name the real version.
func String() string {
	v := Version
	if v == "dev" {
		if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
			v = bi.Main.Version
		}
	}
	return strings.TrimPrefix(v, "v")
}
