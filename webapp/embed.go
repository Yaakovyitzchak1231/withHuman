// Package webapp embeds the built product frontend so a composition can
// serve it: cmd/withhuman hands Assets to runtime.ServerOptions.Frontend.
// The Vite build in frontend/ writes into assets/ (make frontend); the
// hosted edition builds its own overlay bundle and embeds that instead,
// so this package is never linked into a hosted binary.
package webapp

import (
	"embed"
	"io/fs"
)

//go:embed all:assets/*
var assets embed.FS

// Assets is the built frontend, rooted at its index.html.
func Assets() fs.FS {
	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		panic(err)
	}
	return sub
}
