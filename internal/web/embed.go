package web

import (
	"embed"
	"io/fs"
)

//go:embed templates/*.html static/*
var assets embed.FS

// templatesFS exposes only the templates/ subtree to html/template
// parsing.  Returning an io/fs.FS lets tests substitute a writable
// in-memory filesystem if (later) we need to.
func templatesFS() fs.FS {
	sub, err := fs.Sub(assets, "templates")
	if err != nil {
		// embed paths are compile-time constants, so a Sub failure
		// here means the build is broken — panic rather than carrying
		// a partial Server.
		panic("web: templates subtree missing from embedded assets: " + err.Error())
	}
	return sub
}

// staticFS exposes only the static/ subtree, used by /static/* file
// serving.  Same panic-on-malformed-embed rationale as templatesFS.
func staticFS() fs.FS {
	sub, err := fs.Sub(assets, "static")
	if err != nil {
		panic("web: static subtree missing from embedded assets: " + err.Error())
	}
	return sub
}
