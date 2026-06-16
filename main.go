package main

import (
	"context"
	"errors"
	"os"

	"hop.top/foo/cmd/foo/commands"
	"hop.top/kit/go/console/output"
)

var version = "dev"

func main() {
	root := commands.New(version)
	if err := root.Execute(context.Background()); err != nil {
		os.Exit(exitCode(err))
	}
}

// exitCode maps a returned error to a process exit status. kit's
// Root.Execute returns the error rather than os.Exit-ing the semantic
// code, so the adopter owns the mapping: any error carrying an
// *output.Error (via the AsCLIError convention) exits with its
// ExitCode (3 not-found, 4 conflict, 5 unauthorized, …); everything
// else falls back to the generic 1.
func exitCode(err error) int {
	type cliError interface{ AsCLIError() *output.Error }
	var ce cliError
	if errors.As(err, &ce) {
		if e := ce.AsCLIError(); e != nil && e.ExitCode != 0 {
			return e.ExitCode
		}
	}
	return 1
}
