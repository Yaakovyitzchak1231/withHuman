// Package db embeds the application database migrations so every binary
// carries its schema with it, and owns the runner (Apply) that applies a
// migration chain, core's or an edition's own.
package db

import (
	"embed"
	"io/fs"
)

//go:embed migrations/*.sql
var migrations embed.FS

// Migrations returns the application migration files as a file system
// rooted at the directory containing the .sql files.
func Migrations() fs.FS {
	sub, err := fs.Sub(migrations, "migrations")
	if err != nil {
		panic(err)
	}
	return sub
}
