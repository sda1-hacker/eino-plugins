//go:build windows

package computeruse

import (
	"os/exec"
	"syscall"
)

// Windows helper 不弹出额外的 PowerShell 控制台。
func configureProcess(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} }
func killProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return cmd.Process.Kill()
}
