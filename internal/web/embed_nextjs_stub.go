//go:build noweb

package web

import "io/fs"

// nextjsFS returns (nil, false) when built with -tags noweb.
// Use this tag when you want to exclude the embedded web UI (e.g. `go test -tags noweb ./...`).
func nextjsFS() (fs.FS, bool) {
	return nil, false
}
