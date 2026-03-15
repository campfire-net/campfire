package main

import (
	"os"

	"github.com/3dl-dev/campfire/cmd/cf/cmd"
)

func main() {
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
