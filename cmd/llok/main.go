package main

import (
	"os"

	"github.com/muhammadanas/llok/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
