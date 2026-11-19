//go:build !unix

package backend

import (
	"os/exec"
	"time"
)

func prepareProcessGroup(cmd *exec.Cmd) { cmd.WaitDelay = 5 * time.Second }

func killProcessGroup(cmd *exec.Cmd) {}
