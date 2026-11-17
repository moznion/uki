//go:build unix

package runner

import (
	"os/exec"
	"syscall"
	"time"
)

// detachWait is how long a cancelled task gets to exit after SIGTERM before
// its shell is killed.
const detachWait = 10 * time.Second

func detach(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Signal the group, not just the shell: `go test` and the like would
	// otherwise keep running after the task was cancelled.
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = detachWait
}
