// Command warden is a configurable git commit/push gate installed as native git
// hooks. See the package docs under internal/ for the architecture.
package main

import (
	"os"

	"go.klarlabs.de/warden/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args, os.Stdout, os.Stderr))
}
