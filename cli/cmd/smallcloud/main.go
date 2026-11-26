package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/creova-gif/smallcloud/cli"
)

func run(args []string, stateDir string, stdout, stderr io.Writer) int {
	if stateDir == "" {
		stateDir = filepath.Join(os.TempDir(), "smallcloud-state")
	}
	if err := cli.Run(args, stateDir, stdout); err != nil {
		fmt.Fprintf(stderr, "smallcloud: %v\n", err)
		return 1
	}
	return 0
}

func main() {
	os.Exit(run(os.Args[1:], os.Getenv("SMALLCLOUD_STATE_DIR"), os.Stdout, os.Stderr))
}
