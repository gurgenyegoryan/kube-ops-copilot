package cli

import (
	"fmt"
	"os"
)

func Run(args []string) int {
	root := NewRootCmd()
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
