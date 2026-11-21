// Command assay is the assay command line.
package main

import (
	"os"

	"assay/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], cli.DefaultEnv()))
}
