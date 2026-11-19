//go:build !desktop && !bindings

package main

import (
	"fmt"
	"os"
)

// This package is the Wails desktop app (main_desktop.go), which only builds
// with the desktop tag. Without it, point people at the right entry point.
func main() {
	fmt.Fprintln(os.Stderr, `This package is the harness desktop app. Build it with "wails build" or "wails dev".
For the command line, run "go run ./cmd/harness" from app/, or install it with "go install ./cmd/harness".`)
	os.Exit(64)
}
