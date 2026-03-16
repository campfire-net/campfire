package main

import (
	"os"

	"github.com/campfire-net/campfire/cmd/cf/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
