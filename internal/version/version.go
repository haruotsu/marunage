package version

import "runtime/debug"

// version is overridden at build time via:
//
//	-ldflags "-X github.com/haruotsu/marunage/internal/version.version=vX.Y.Z"
//
// When empty (e.g. `go install ...@latest`), Version() falls back to BuildInfo.
var version = ""

// Version returns the current build version, preferring the value injected
// via -ldflags. When that is empty, it consults runtime/debug.BuildInfo so
// that `go install`-built binaries still report a meaningful identifier.
func Version() string {
	if version != "" {
		return version
	}
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
		for _, s := range info.Settings {
			if s.Key == "vcs.revision" && s.Value != "" {
				return s.Value
			}
		}
	}
	return "dev"
}
