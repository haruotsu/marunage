// Command marunage is the entrypoint for the marunage CLI.
//
// All subcommand wiring lives in internal/cli; this file exists only to bind
// the process to that package.
package main

import (
	"os"

	"github.com/haruotsu/marunage/internal/cli"
)

func main() {
	os.Exit(cli.Execute(os.Args[1:], os.Stdout, os.Stderr))
}
