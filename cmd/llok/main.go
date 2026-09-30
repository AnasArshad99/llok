package main

import (
	"os"

	"github.com/AnasArshad99/llok/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
