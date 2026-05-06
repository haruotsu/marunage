//go:build nextjs

package web

import (
	"embed"
	"io/fs"
)

//go:embed out
var nextjsAssets embed.FS

func nextjsFS() (fs.FS, bool) {
	sub, err := fs.Sub(nextjsAssets, "out")
	if err != nil {
		return nil, false
	}
	return sub, true
}
