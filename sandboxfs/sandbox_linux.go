//go:build linux

package sandboxfs

import (
	"context"
	"os"
	"os/exec"
	"syscall"
)

const bwrapPath = "/usr/bin/bwrap"

func platformCommand(ctx context.Context, workspace, command string, allowNetwork bool) (sandboxCommand, func() error, error) {
	// 不绑定宿主机 / 或 HOME，网络 Namespace 默认隔离。
	args := []string{
		"--unshare-all", "--die-with-parent", "--new-session", "--cap-drop", "ALL",
		"--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp",
		"--dir", "/tmp/home",
	}
	for _, dir := range []string{"/usr", "/bin", "/lib", "/lib64"} {
		if st, err := os.Stat(dir); err == nil && st.IsDir() {
			args = append(args, "--ro-bind", dir, dir)
		}
	}
	if allowNetwork {
		// 只恢复宿主网络，其余 Namespace 和文件系统限制仍然保留。
		args = append(args, "--share-net")
		for _, p := range []string{"/etc/resolv.conf", "/etc/hosts", "/etc/nsswitch.conf",
			"/etc/ssl/certs", "/etc/ssl/cert.pem", "/etc/pki/tls/certs", "/etc/pki/ca-trust/extracted"} {
			args = append(args, "--ro-bind-try", p, p)
		}
	}
	args = append(args,
		"--bind", workspace, workspace, "--chdir", workspace, "--clearenv",
		"--setenv", "PATH", "/usr/local/bin:/usr/bin:/bin",
		"--setenv", "HOME", "/tmp/home", "--setenv", "TMPDIR", "/tmp",
		"--setenv", "XDG_CACHE_HOME", "/tmp/cache",
		"--setenv", "npm_config_cache", "/tmp/cache/npm",
		"--setenv", "PIP_CACHE_DIR", "/tmp/cache/pip",
		"--setenv", "LANG", "C", "--setenv", "LC_ALL", "C",
		"--", "/bin/sh", "-c", command,
	)
	cmd := exec.CommandContext(ctx, bwrapPath, args...)
	// --clearenv 只管理沙箱环境；启动器也不应继承宿主机密钥或动态链接器变量。
	cmd.Env = []string{"PATH=/usr/bin:/bin", "LANG=C", "LC_ALL=C"}
	cmd.Dir = "/"
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	return execCommand{cmd}, func() error { return nil }, nil
}

// 非阻塞打开后再检查文件类型，避免 FIFO 在 Stat 之前阻塞。
func openRootRead(root *os.Root, rel string) (*os.File, error) {
	return root.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
}
