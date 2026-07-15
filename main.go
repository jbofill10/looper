package main

import (
	"fmt"
	"os"

	"github.com/jbofill10/looper/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
