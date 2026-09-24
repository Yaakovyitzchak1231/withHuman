// withhuman-cli builds the `withhuman` command: the guided setup, the
// agent commands and embedded AAP hooks. It is
// distributed as a standalone binary and never links the server.
package main

import (
	"os"

	"github.com/withHumanAI/withHuman/internal/cli"
)

// version is set by the release build with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	os.Exit(cli.Main(version, os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
