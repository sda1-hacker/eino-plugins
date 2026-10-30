//go:build windows

package sandboxfs

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"golang.org/x/sys/windows"
)

// 在启动时设置身份、句柄和 Job；
// LPAC 额外限制读取和网络，不采用仅检查写权限的 WRITE_RESTRICTED。
const (
	attributeSecurityCapabilities = 0x20009
	attributeJobList              = 0x2000d
	attributeAllPackagesPolicy    = 0x2000f
)

var (
	userenv       = windows.NewLazySystemDLL("userenv.dll")
	createProfile = userenv.NewProc("CreateAppContainerProfile")
	deleteProfile = userenv.NewProc("DeleteAppContainerProfile")
	profileFolder = userenv.NewProc("GetAppContainerFolderPath")
	user32        = windows.NewLazySystemDLL("user32.dll")
	createDesktop = user32.NewProc("CreateDesktopW")
	closeDesktop  = user32.NewProc("CloseDesktop")
	setSecurity   = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtSetSecurityObject")
	windowsACLMu  sync.Mutex // Get/merge/set 必须串行；撤销时只删除本次 SID。
)

type securityCapabilities struct {
	sid          *windows.SID
	capabilities *windows.SIDAndAttributes
	count        uint32
	reserved     uint32
}

type windowsCommand struct {
	ctx                context.Context
	workspace, command string
	allowNetwork       bool
	name               *uint16
	sid                *windows.SID
	guard              *Backend
	root               windows.Handle
	grants             []windows.Handle
	rootGranted        bool
	tmp                string
	desktop            windows.Handle
	process            *os.Process
	stop               func() bool
	mu                 sync.Mutex
	job                windows.Handle
	terminated         bool
}

func openRootRead(root *os.Root, rel string) (*os.File, error) {
	return root.Open(rel)
}

func platformCommand(ctx context.Context, workspace, command string, allowNetwork bool) (sandboxCommand, func() error, error) {
	if strings.ContainsRune(command, 0) {
		return nil, nil, ErrDenied
	}
	// UNC/设备路径不能提供本实现所需的本地 ACL 隔离。
	volume := filepath.VolumeName(workspace)
	if len(volume) != 2 || volume[1] != ':' {
		return nil, nil, fmt.Errorf("%w: Windows Shell 工作区必须位于本地磁盘", ErrShellUnavailable)
	}
	if windows.RtlGetVersion().BuildNumber < 15063 {
		return nil, nil, fmt.Errorf("%w: 需要 Windows 10 1703 或更新版本", ErrShellUnavailable)
	}
	c := &windowsCommand{ctx: ctx, workspace: workspace, command: command, allowNetwork: allowNetwork}
	if err := c.prepare(); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrShellUnavailable, errors.Join(err, c.cleanup()))
	}
	return c, c.cleanup, nil
}

func (c *windowsCommand) prepare() error {
	for _, proc := range []*windows.LazyProc{createProfile, deleteProfile, profileFolder, createDesktop, closeDesktop, setSecurity} {
		if err := proc.Find(); err != nil {
			return err
		}
	}
	c.name = windows.StringToUTF16Ptr("sandboxfs-" + rand.Text())
	hr, _, _ := createProfile.Call(uintptr(unsafe.Pointer(c.name)), uintptr(unsafe.Pointer(c.name)),
		uintptr(unsafe.Pointer(c.name)), 0, 0, uintptr(unsafe.Pointer(&c.sid)))
	if int32(hr) < 0 {
		return fmt.Errorf("CreateAppContainerProfile: HRESULT 0x%08x", uint32(hr))
	}
	r, err := os.OpenRoot(c.workspace)
	if err != nil {
		return err
	}
	c.guard = &Backend{root: r, workspace: c.workspace}
	c.root, err = windows.CreateFile(windows.StringToUTF16Ptr(c.workspace), windows.FILE_GENERIC_READ|windows.FILE_GENERIC_EXECUTE|windows.WRITE_DAC,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		c.root = 0
		return err
	}
	var flags uint32
	if err := windows.GetVolumeInformationByHandle(c.root, nil, 0, nil, nil, &flags, nil, 0); err != nil {
		return err
	}
	if flags&windows.FILE_PERSISTENT_ACLS == 0 {
		return errors.New("工作区磁盘不支持持久 ACL")
	}
	if err := changeSandboxACL(c.root, c.sid, false); err != nil {
		return fmt.Errorf("授权工作区 %q: %w", c.workspace, err)
	}
	c.rootGranted = true
	// 逐个对象更新 ACL，不调用会递归传播权限的 SetSecurityInfo。
	// 用 Root 遍历，再用相对目录句柄及 OBJ_DONT_REPARSE 打开，绝不授权链接目标。
	if err := c.guard.walk(c.ctx, ".", func(rel string, _ os.FileInfo) error {
		h, err := c.openACLFile(rel)
		if err != nil {
			return fmt.Errorf("打开工作区对象 %q: %w", rel, err)
		}
		if err := changeSandboxACL(h, c.sid, false); err != nil {
			windows.CloseHandle(h)
			return fmt.Errorf("授权工作区对象 %q: %w", rel, err)
		}
		c.grants = append(c.grants, h)
		return nil
	}); err != nil {
		return err
	}
	// 使用 Windows 为此身份创建的私有目录，不向宿主 TMPDIR 额外授予权限。
	var folder *uint16
	hr, _, _ = profileFolder.Call(uintptr(unsafe.Pointer(windows.StringToUTF16Ptr(c.sid.String()))), uintptr(unsafe.Pointer(&folder)))
	if int32(hr) < 0 {
		return fmt.Errorf("GetAppContainerFolderPath: HRESULT 0x%08x", uint32(hr))
	}
	c.tmp = filepath.Join(windows.UTF16PtrToString(folder), "Temp")
	windows.CoTaskMemFree(unsafe.Pointer(folder))
	if err := os.MkdirAll(c.tmp, 0700); err != nil {
		return err
	}
	tokenUser, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;SY)(A;;GA;;;" + tokenUser.User.Sid.String() + ")(A;;GA;;;" + c.sid.String() + ")")
	if err != nil {
		return err
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	desktop, _, callErr := createDesktop.Call(uintptr(unsafe.Pointer(c.name)), 0, 0, 0, windows.GENERIC_ALL, uintptr(unsafe.Pointer(&sa)))
	if desktop == 0 {
		return fmt.Errorf("CreateDesktopW: %w", callErr)
	}
	c.desktop = windows.Handle(desktop)
	c.job, err = windows.CreateJobObject(nil, nil)
	if err != nil {
		return err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	_, err = windows.SetInformationJobObject(c.job, windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits)))
	return err
}

func (c *windowsCommand) openACLFile(rel string) (windows.Handle, error) {
	name, err := windows.NewNTUnicodeString(filepath.FromSlash(rel))
	if err != nil {
		return 0, err
	}
	oa := windows.OBJECT_ATTRIBUTES{Length: uint32(unsafe.Sizeof(windows.OBJECT_ATTRIBUTES{})),
		RootDirectory: c.root, ObjectName: name, Attributes: windows.OBJ_DONT_REPARSE | windows.OBJ_CASE_INSENSITIVE}
	var h windows.Handle
	var status windows.IO_STATUS_BLOCK
	err = windows.NtCreateFile(&h, windows.READ_CONTROL|windows.WRITE_DAC|windows.FILE_READ_ATTRIBUTES|windows.SYNCHRONIZE, &oa, &status, nil, 0,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		windows.FILE_OPEN, windows.FILE_OPEN_REPARSE_POINT|windows.FILE_SYNCHRONOUS_IO_NONALERT, 0, 0)
	return h, err
}

func changeSandboxACL(h windows.Handle, sid *windows.SID, revoke bool) error {
	windowsACLMu.Lock()
	defer windowsACLMu.Unlock()
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return err
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0 {
		return fmt.Errorf("%w: 不对重解析点授予权限", ErrDenied)
	}
	if revoke {
		var standard struct {
			allocation, size         int64
			links                    uint32
			deletePending, directory byte
		}
		if err := windows.GetFileInformationByHandleEx(h, windows.FileStandardInfo,
			(*byte)(unsafe.Pointer(&standard)), uint32(unsafe.Sizeof(standard))); err != nil {
			return err
		}
		if standard.deletePending != 0 {
			return nil // 已删除/被原子替换的对象会在句柄关闭时消失。
		}
	}
	sd, err := windows.GetSecurityInfo(h, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	if acl == nil {
		return errors.New("拒绝修改没有 DACL 的文件")
	}
	entry := windows.EXPLICIT_ACCESS{
		AccessPermissions: windows.FILE_GENERIC_READ | windows.FILE_GENERIC_WRITE | windows.FILE_GENERIC_EXECUTE | windows.DELETE,
		AccessMode:        windows.GRANT_ACCESS,
		Trustee:           windows.TRUSTEE{TrusteeForm: windows.TRUSTEE_IS_SID, TrusteeValue: windows.TrusteeValueFromSID(sid)},
	}
	if info.FileAttributes&windows.FILE_ATTRIBUTE_DIRECTORY != 0 {
		entry.Inheritance = windows.OBJECT_INHERIT_ACE | windows.CONTAINER_INHERIT_ACE
	}
	if revoke {
		entry.AccessMode = windows.REVOKE_ACCESS
	}
	acl, err = windows.ACLFromEntries([]windows.EXPLICIT_ACCESS{entry}, acl)
	if err != nil {
		return err
	}
	absolute, err := sd.ToAbsolute()
	if err != nil {
		return err
	}
	if err := absolute.SetDACL(acl, true, false); err != nil {
		return err
	}
	status, _, _ := setSecurity.Call(uintptr(h), windows.DACL_SECURITY_INFORMATION, uintptr(unsafe.Pointer(absolute)))
	if int32(status) < 0 {
		return fmt.Errorf("NtSetSecurityObject: %w", windows.NTStatus(status))
	}
	return nil
}

func (c *windowsCommand) Start(output *os.File) error {
	if err := c.ctx.Err(); err != nil {
		return err
	}
	systemDir, err := windows.GetSystemDirectory()
	if err != nil {
		return err
	}
	windowsDir, err := windows.GetWindowsDirectory()
	if err != nil {
		return err
	}
	cmdPath := filepath.Join(systemDir, "cmd.exe")
	// cmd 的 /c 最后一个参数必须原样保留，不能使用 MSVCRT 参数转义。
	line, err := windows.UTF16PtrFromString(windows.EscapeArg(cmdPath) + ` /d /s /c ""` + filepath.Join(systemDir, "chcp.com") + `" 65001 >nul & ` + c.command + `"`)
	if err != nil {
		return err
	}
	attrs, err := windows.NewProcThreadAttributeList(4)
	if err != nil {
		return err
	}
	defer attrs.Delete()
	capabilities := securityCapabilities{sid: c.sid}
	var caps []windows.SIDAndAttributes
	if c.allowNetwork {
		for _, typ := range []windows.WELL_KNOWN_SID_TYPE{windows.WinCapabilityInternetClientSid, windows.WinCapabilityInternetClientServerSid, windows.WinCapabilityPrivateNetworkClientServerSid} {
			sid, err := windows.CreateWellKnownSid(typ)
			if err != nil {
				return err
			}
			caps = append(caps, windows.SIDAndAttributes{Sid: sid, Attributes: windows.SE_GROUP_ENABLED})
		}
		capabilities.capabilities, capabilities.count = &caps[0], uint32(len(caps))
	}
	// LPAC 不继承 ALL APPLICATION PACKAGES 的广泛读取权限。
	policy := uint32(1) // PROCESS_CREATION_ALL_APPLICATION_PACKAGES_OPT_OUT
	if err := attrs.Update(attributeSecurityCapabilities, unsafe.Pointer(&capabilities), unsafe.Sizeof(capabilities)); err != nil {
		return err
	}
	if err := attrs.Update(attributeAllPackagesPolicy, unsafe.Pointer(&policy), unsafe.Sizeof(policy)); err != nil {
		return err
	}
	if err := attrs.Update(attributeJobList, unsafe.Pointer(&c.job), unsafe.Sizeof(c.job)); err != nil {
		return err
	}
	input, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer input.Close()
	// 防止同一 Go 进程里的其他 StartProcess 继承短暂设为可继承的管道。
	syscall.ForkLock.Lock()
	defer syscall.ForkLock.Unlock()
	var handles [2]windows.Handle
	for i, file := range []*os.File{input, output} {
		if err := windows.DuplicateHandle(windows.CurrentProcess(), windows.Handle(file.Fd()), windows.CurrentProcess(),
			&handles[i], 0, true, windows.DUPLICATE_SAME_ACCESS); err != nil {
			return err
		}
		defer windows.CloseHandle(handles[i])
	}
	if err := attrs.Update(windows.PROC_THREAD_ATTRIBUTE_HANDLE_LIST, unsafe.Pointer(&handles[0]), unsafe.Sizeof(handles)); err != nil {
		return err
	}
	env := []string{"SystemRoot=" + windowsDir, "WINDIR=" + windowsDir, "COMSPEC=" + cmdPath,
		"PATH=" + systemDir + ";" + windowsDir, "PATHEXT=.COM;.EXE;.BAT;.CMD",
		"HOME=" + c.tmp, "USERPROFILE=" + c.tmp, "APPDATA=" + c.tmp, "LOCALAPPDATA=" + c.tmp,
		"TEMP=" + c.tmp, "TMP=" + c.tmp, "XDG_CACHE_HOME=" + filepath.Join(c.tmp, "cache"),
		"npm_config_cache=" + filepath.Join(c.tmp, "cache", "npm"), "PIP_CACHE_DIR=" + filepath.Join(c.tmp, "cache", "pip")}
	sort.Slice(env, func(i, j int) bool { return strings.ToUpper(env[i]) < strings.ToUpper(env[j]) })
	block := utf16.Encode([]rune(strings.Join(env, "\x00") + "\x00\x00"))
	startup := windows.StartupInfoEx{ProcThreadAttributeList: attrs.List()}
	startup.Cb = uint32(unsafe.Sizeof(startup))
	startup.Desktop, startup.Flags = c.name, windows.STARTF_USESTDHANDLES|windows.STARTF_USESHOWWINDOW
	startup.ShowWindow = windows.SW_HIDE
	startup.StdInput, startup.StdOutput, startup.StdErr = handles[0], handles[1], handles[1]
	var pi windows.ProcessInformation
	flags := uint32(windows.CREATE_SUSPENDED | windows.CREATE_NEW_CONSOLE | windows.CREATE_UNICODE_ENVIRONMENT | windows.EXTENDED_STARTUPINFO_PRESENT)
	if err := windows.CreateProcess(windows.StringToUTF16Ptr(cmdPath), line, nil, nil, true, flags,
		&block[0], windows.StringToUTF16Ptr(c.workspace), &startup.StartupInfo, &pi); err != nil {
		return fmt.Errorf("CreateProcessW(LPAC): %w", err)
	}
	defer windows.CloseHandle(pi.Thread)
	defer windows.CloseHandle(pi.Process)
	c.process, err = os.FindProcess(int(pi.ProcessId))
	if err != nil {
		_ = c.Cancel()
		return err
	}
	c.stop = context.AfterFunc(c.ctx, func() { _ = c.Cancel() })
	if err := c.ctx.Err(); err != nil {
		_ = c.Cancel()
		_, _ = c.process.Wait()
		return err
	}
	if _, err := windows.ResumeThread(pi.Thread); err != nil {
		_ = c.Cancel()
		_, _ = c.process.Wait()
		return err
	}
	return nil
}

func (c *windowsCommand) Wait() error {
	defer c.stop()
	state, err := c.process.Wait()
	if err != nil {
		return err
	}
	if !state.Success() {
		return &exec.ExitError{ProcessState: state}
	}
	return nil
}

func (c *windowsCommand) Cancel() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.job == 0 {
		return os.ErrProcessDone
	}
	if c.terminated {
		return nil
	}
	err := windows.TerminateJobObject(c.job, 1)
	if err == nil {
		c.terminated = true
	}
	return err
}

func (c *windowsCommand) cleanup() error {
	var errs []error
	if c.stop != nil {
		c.stop()
	}
	if c.job != 0 {
		errs = append(errs, c.Cancel())
		// 等待后代真正退出后再撤销授权，不能只等待 cmd.exe。
		deadline := time.Now().Add(3 * time.Second)
		for {
			var accounting struct {
				times                                 [4]int64
				pageFaults, total, active, terminated uint32
			}
			err := windows.QueryInformationJobObject(c.job, windows.JobObjectBasicAccountingInformation,
				uintptr(unsafe.Pointer(&accounting)), uint32(unsafe.Sizeof(accounting)), nil)
			if err != nil || accounting.active == 0 {
				errs = append(errs, err)
				break
			}
			if time.Now().After(deadline) {
				errs = append(errs, errors.New("Windows Job 子进程清理超时"))
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		c.mu.Lock()
		errs = append(errs, windows.CloseHandle(c.job))
		c.job = 0
		c.mu.Unlock()
	}
	if c.rootGranted {
		errs = append(errs, changeSandboxACL(c.root, c.sid, true))
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		// 包括命令新建的文件；原有对象另保留句柄，重命名后仍可撤销授权。
		errs = append(errs, c.guard.walk(ctx, ".", func(rel string, _ os.FileInfo) error {
			h, err := c.openACLFile(rel)
			if err != nil {
				return err
			}
			defer windows.CloseHandle(h)
			return changeSandboxACL(h, c.sid, true)
		}))
		cancel()
	}
	for _, h := range c.grants {
		errs = append(errs, changeSandboxACL(h, c.sid, true), windows.CloseHandle(h))
	}
	if c.root != 0 {
		errs = append(errs, windows.CloseHandle(c.root))
	}
	if c.guard != nil {
		errs = append(errs, c.guard.Close())
	}
	if c.desktop != 0 {
		if ok, _, err := closeDesktop.Call(uintptr(c.desktop)); ok == 0 {
			errs = append(errs, fmt.Errorf("CloseDesktop: %w", err))
		}
	}
	if c.tmp != "" {
		errs = append(errs, os.RemoveAll(c.tmp))
	}
	if c.sid != nil {
		if hr, _, _ := deleteProfile.Call(uintptr(unsafe.Pointer(c.name))); int32(hr) < 0 {
			errs = append(errs, fmt.Errorf("DeleteAppContainerProfile: HRESULT 0x%08x", uint32(hr)))
		}
		errs = append(errs, windows.FreeSid(c.sid))
	}
	return errors.Join(errs...)
}
