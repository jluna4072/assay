// Command harness is the harness command line.
package main

import (
	"os"

	"harness/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
