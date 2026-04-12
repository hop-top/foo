package main

import (
	"context"
	"os"

	"hop.top/foo/cmd/foo/commands"
)

func main() {
	root := commands.New()
	if err := root.Execute(context.Background()); err != nil {
		os.Exit(1)
	}
}
