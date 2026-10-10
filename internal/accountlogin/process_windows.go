package accountlogin

import (
	"os/exec"
	"strconv"
)

func configureCancellation(cmd *exec.Cmd) {
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		if err := exec.Command("taskkill", "/PID", strconv.Itoa(cmd.Process.Pid), "/T", "/F").Run(); err != nil {
			return cmd.Process.Kill()
		}
		return nil
	}
}
