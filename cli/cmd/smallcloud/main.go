package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/creova-gif/smallcloud/cli"
)

func main() {
	stateDir := os.Getenv("SMALLCLOUD_STATE_DIR")
	if stateDir == "" {
		stateDir = filepath.Join(os.TempDir(), "smallcloud-state")
	}
	if err := cli.Run(os.Args[1:], stateDir, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "smallcloud: %v\n", err)
		os.Exit(1)
	}
}
