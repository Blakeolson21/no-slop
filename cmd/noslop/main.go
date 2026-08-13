package main

import (
	"context"
	"os"

	slopcli "github.com/Blakeolson21/no-slop/internal/slop/cli"
)

func main() {
	os.Exit(slopcli.Run(context.Background(), os.Args[1:], os.Stdout, os.Stderr, slopcli.Options{}))
}
