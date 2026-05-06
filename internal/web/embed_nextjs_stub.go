//go:build !nextjs

package web

import "io/fs"

func nextjsFS() (fs.FS, bool) {
	return nil, false
}
