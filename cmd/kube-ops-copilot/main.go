package main

import (
	"os"

	"github.com/gurgenyegoryan/kube-ops-copilot/internal/cli"
)

func main() {
	exitCode := cli.Run(os.Args[1:])
	os.Exit(exitCode)
}
