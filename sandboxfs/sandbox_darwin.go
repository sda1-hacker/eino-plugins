//go:build darwin

package sandboxfs

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

const seatbeltPath = "/usr/bin/sandbox-exec"

// deny default。系统只读路径依赖实际工具链，部署时需要针对 macOS 版本验证。
const seatbeltProfile = `(version 1)
(deny default)
; Shell 启动需要读取根目录本身；literal 不开放根目录下的文件树。
(allow file-read* (literal "/"))
; macOS /bin/sh 选择器需要检查这个链接的元数据。
(allow file-read-metadata (literal "/private/var/select/sh"))
(allow process-exec)
(allow process-fork)
; 运行时的 sysconf 等需要查询系统参数；不允许修改参数。
(allow sysctl-read)
(allow file-read* (subpath "/usr") (subpath "/bin") (subpath "/System/Library"))
(allow file-read* (subpath (param "WORKSPACE")))
(allow file-write* (subpath (param "WORKSPACE")))
(allow file-read* file-write* (subpath (param "TEMP")))
; 普通重定向需要 /dev/null，不开放其他宿主机设备。
(allow file-read* file-write-data (literal "/dev/null"))
`

func platformCommand(ctx context.Context, workspace, command string, allowNetwork bool) (sandboxCommand, func() error, error) {
	// 每次运行使用独立的临时目录；不允许沙箱读取其他会话临时文件。
	tmp, err := os.MkdirTemp("", "sandboxfs-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() error { return os.RemoveAll(tmp) }
	// Seatbelt 按物理路径匹配，不能直接使用 /var 等符号链接别名。
	canonical, err := filepath.Abs(tmp)
	if err == nil {
		canonical, err = filepath.EvalSymlinks(canonical)
	}
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	tmp = canonical
	args := []string{"-D", "WORKSPACE=" + workspace, "-D", "TEMP=" + tmp}
	var policy strings.Builder
	policy.WriteString(seatbeltProfile)
	if allowNetwork {
		// DNS 和系统 TLS 校验还需要访问系统服务，单独允许 socket 不足以联网。
		policy.WriteString(`(allow network*)
(allow mach-lookup
    (global-name "com.apple.dnssd.service")
    (global-name "com.apple.SystemConfiguration.DNSConfiguration")
    (global-name "com.apple.SystemConfiguration.SCNetworkReachability")
    (global-name "com.apple.trustd")
    (global-name "com.apple.trustd.agent"))
(allow file-read*
    (literal "/private/etc/resolv.conf") (literal "/private/var/run/resolv.conf")
    (literal "/private/etc/hosts") (literal "/private/etc/ssl/cert.pem"))
(allow file-read-metadata
    (literal "/etc") (literal "/var") (literal "/var/run")
    (literal "/private") (literal "/private/etc") (literal "/private/etc/ssl")
    (literal "/private/var") (literal "/private/var/run")
    (literal "/private/var/run/mDNSResponder"))
`)
	} else {
		policy.WriteString("(deny network*)\n")
	}
	// mkdir -p、realpath 等需要检查祖先目录；只开放元数据，不开放目录内容。
	for _, dir := range []string{workspace, tmp} {
		for parent := filepath.Dir(dir); parent != "/"; parent = filepath.Dir(parent) {
			name := fmt.Sprintf("PARENT_%d", len(args))
			args = append(args, "-D", name+"="+parent)
			fmt.Fprintf(&policy, "\n(allow file-read-metadata (literal (param %q)))\n", name)
		}
	}
	profile := filepath.Join(tmp, "policy.sb")
	if err := os.WriteFile(profile, []byte(policy.String()), 0600); err != nil {
		cleanup()
		return nil, nil, err
	}
	args = append(args, "-f", profile, "/bin/sh", "-c", command)
	cmd := exec.CommandContext(ctx, seatbeltPath, args...)
	cmd.Dir = workspace
	cmd.Env = []string{
		"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "HOME=" + tmp,
		"TMPDIR=" + tmp, "LANG=C", "LC_ALL=C",
		"XDG_CACHE_HOME=" + filepath.Join(tmp, "cache"),
		"npm_config_cache=" + filepath.Join(tmp, "cache", "npm"),
		"PIP_CACHE_DIR=" + filepath.Join(tmp, "cache", "pip"),
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if err == syscall.ESRCH {
			return os.ErrProcessDone
		}
		return err
	}
	return execCommand{cmd}, cleanup, nil
}

// 非阻塞打开后再检查文件类型，避免 FIFO 在 Stat 之前阻塞。
func openRootRead(root *os.Root, rel string) (*os.File, error) {
	return root.OpenFile(rel, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
}
