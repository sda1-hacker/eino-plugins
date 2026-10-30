//go:build !darwin && !windows

package computeruse

import (
	"os/exec"
	"syscall"
)

func newNativeMacOSDriver() (nativeMacOSDriver, error) {
	return nil, failure(ErrProviderUnavailable, "macOS 原生后端仅在 macOS 可用")
}

// 独立进程组使取消可以清理 helper 派生的进程，避免只杀掉外层 CLI。
func configureProcess(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func killProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
