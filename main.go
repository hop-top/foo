package main

import (
	"context"
	"os"

	"hop.top/foo/cmd/foo/commands"
)

var version = "dev"

func main() {
	root := commands.New(version)
	if err := root.Execute(context.Background()); err != nil {
		os.Exit(1)
	}
}
