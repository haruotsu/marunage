package version

// version is the build-time injected version string.
// Override at build time with: -ldflags "-X github.com/haruotsu/marunage/internal/version.version=vX.Y.Z"
var version = "dev"

// Version returns the current build version.
func Version() string {
	return version
}
