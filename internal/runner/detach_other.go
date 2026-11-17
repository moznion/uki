//go:build !unix

package runner

import "os/exec"

// detach is a no-op where process groups are not available: cancelling
// stops the shell only.
func detach(*exec.Cmd) {}
