//go:build !windows

package accountlogin

import (
	"os/exec"
	"syscall"
)

func configureCancellation(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		// Terminate Chromium and its driver as well as the Python bridge.
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
}
