// Command marunage is the entrypoint for the marunage CLI.
//
// PR-01 provides only --version. The full sub-command set (cobra-based) lands in PR-02.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/haruotsu/marunage/internal/version"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("marunage", flag.ContinueOnError)
	fs.SetOutput(stderr)

	showVersion := fs.Bool("version", false, "print version and exit")

	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *showVersion {
		fmt.Fprintln(stdout, version.Version())
		return 0
	}

	return 0
}
