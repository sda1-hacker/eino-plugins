package computeruse

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed assets/windows-uia-helper.ps1
var windowsUIAScript string

// WindowsConfig 配置后台 UIA；原项目禁用的 SendInput 等前台注入不会启用。
type WindowsConfig struct {
	Command        string
	Platform       string
	Runner         CommandRunner
	HelperDir      string
	Timeout        time.Duration
	MaxResultBytes int64
}
type WindowsProvider struct {
	mu        sync.Mutex
	config    WindowsConfig
	runner    CommandRunner
	helperDir string
	ownDir    bool
}

func NewWindowsProvider(config *WindowsConfig) *WindowsProvider {
	c := WindowsConfig{}
	if config != nil {
		c = *config
	}
	if c.Platform == "" {
		c.Platform = runtime.GOOS
	}
	if c.Platform == "win32" {
		c.Platform = "windows"
	}
	if c.Command == "" {
		c.Command = "powershell.exe"
		if runtime.GOOS == "windows" {
			root := os.Getenv("SystemRoot")
			if root == "" {
				root = `C:\Windows`
			}
			c.Command = filepath.Join(root, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
		}
	}
	if c.Runner == nil {
		c.Runner = ExecRunner{}
	}
	if c.Timeout <= 0 {
		c.Timeout = 30 * time.Second
	}
	if c.MaxResultBytes <= 0 {
		c.MaxResultBytes = 32 << 20
	}
	return &WindowsProvider{config: c, runner: c.Runner, helperDir: c.HelperDir}
}
func (p *WindowsProvider) ID() string { return "windows:uia" }
func (p *WindowsProvider) Capabilities() Capabilities {
	return Capabilities{
		Platform:          "windows",
		ObservationModes:  []string{"vision-native", "semantic-only"},
		Screenshot:        true,
		AccessibilityTree: true,
		ElementActions:    true,
		BackgroundControl: "partial",
		PointClick:        "unsupported",
		Drag:              "unsupported",
		TextInput:         "semantic",
		KeyboardInput:     "unsupported",
	}
}
func (p *WindowsProvider) windows() error {
	if p.config.Platform != "windows" {
		return failure(ErrProviderUnavailable, "Windows UIA 仅在 Windows 可用")
	}
	return nil
}
func (p *WindowsProvider) prepareHelper() (string, error) {
	if p.helperDir == "" {
		dir, err := os.MkdirTemp("", "computer-use-uia-")
		if err != nil {
			return "", err
		}
		p.helperDir = dir
		p.ownDir = true
	}
	if err := os.MkdirAll(p.helperDir, 0700); err != nil {
		return "", err
	}
	hash := sha256.Sum256([]byte(windowsUIAScript))
	path := filepath.Join(p.helperDir, "windows-uia-helper-"+hex.EncodeToString(hash[:8])+".ps1")
	if b, err := os.ReadFile(path); err == nil && string(b) == windowsUIAScript {
		return path, nil
	}
	f, err := os.CreateTemp(p.helperDir, "helper-*.ps1")
	if err != nil {
		return "", err
	}
	temp := f.Name()
	defer os.Remove(temp)
	if _, err = f.WriteString(windowsUIAScript); err != nil {
		_ = f.Close()
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	if err = os.Rename(temp, path); err != nil {
		return "", err
	}
	return path, nil
}
func preview(s string) string {
	if len(s) > 2048 {
		return s[:2048] + "…"
	}
	return s
}
func (p *WindowsProvider) helper(ctx context.Context, payload any) (map[string]any, error) {
	path, err := p.prepareHelper()
	if err != nil {
		return nil, failure(ErrProviderCrashed, "无法准备 Windows helper: "+err.Error())
	}
	runDir, err := os.MkdirTemp(p.helperDir, "request-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(runDir)
	requestPath := filepath.Join(runDir, "request.json")
	resultPath := filepath.Join(runDir, "result.json")
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if err = os.WriteFile(requestPath, b, 0600); err != nil {
		return nil, err
	}
	r, err := p.runner.Run(ctx, Command{
		Name: p.config.Command,
		Args: []string{
			"-NoLogo",
			"-NoProfile",
			"-NonInteractive",
			"-ExecutionPolicy",
			"Bypass",
			"-File",
			path,
			"-RequestPath",
			requestPath,
			"-ResultPath",
			resultPath,
		},
		Timeout:        p.config.Timeout,
		MaxOutputBytes: 64 << 10,
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return nil, &ComputerUseError{Code: ErrProviderCrashed, Message: "Windows helper 无法执行: " + err.Error(), Cause: err}
	}
	diagnostics := map[string]any{"stdoutPreview": preview(r.Stdout), "stderrPreview": preview(r.Stderr), "exitCode": r.ExitCode}
	if r.ExitCode != 0 {
		return nil, failure(ErrProviderCrashed, "Windows helper 退出异常", diagnostics)
	}
	stat, err := os.Stat(resultPath)
	if err != nil {
		return nil, failure(ErrProviderCrashed, "Windows helper 没有写入结果 JSON", diagnostics)
	}
	diagnostics["resultSize"] = stat.Size()
	if stat.Size() > p.config.MaxResultBytes {
		return nil, failure(ErrProviderCrashed, "Windows helper 结果超过大小限制", diagnostics)
	}
	b, err = os.ReadFile(resultPath)
	if err != nil {
		return nil, err
	}
	value, err := decodeNative(string(b))
	if err != nil {
		diagnostics["resultPreview"] = preview(string(b))
		return nil, failure(ErrProviderCrashed, "Windows helper 返回无效 JSON", diagnostics)
	}
	m := object(value)
	if !boolean(m["ok"]) {
		code := ErrorCode(str(m["errorCode"]))
		if code == "" {
			code = ErrProviderCrashed
		}
		return nil, failure(code, firstString(m["message"], "Windows UIA 动作失败"), object(m["details"]))
	}
	return object(m["data"]), nil
}
func (p *WindowsProvider) GetStatus(ctx context.Context) (ProviderStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.config.Platform != "windows" {
		return ProviderStatus{Reason: "unsupported-platform"}, nil
	}
	m, err := p.helper(ctx, map[string]any{"command": "status"})
	if err != nil {
		if ctx.Err() != nil {
			return ProviderStatus{}, ctx.Err()
		}
		return ProviderStatus{Reason: "status-failed", Details: map[string]any{"error": err.Error()}}, nil
	}
	available := true
	if b := boolPointer(m["available"]); b != nil {
		available = *b
	}
	return ProviderStatus{Available: available}, nil
}
func (p *WindowsProvider) RequestPermissions(ctx context.Context) (ProviderStatus, error) {
	return p.GetStatus(ctx)
}
func (p *WindowsProvider) ListApps(ctx context.Context) ([]App, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.windows(); err != nil {
		return nil, err
	}
	m, err := p.helper(ctx, map[string]any{"command": "list_apps"})
	if err != nil {
		return nil, err
	}
	return appsOf(m), nil
}
func (p *WindowsProvider) CreateLease(ctx context.Context, t Target) (ProviderLease, error) {
	if err := ctx.Err(); err != nil {
		return ProviderLease{}, err
	}
	if err := p.windows(); err != nil {
		return ProviderLease{}, err
	}
	pid := t.PID
	if pid == 0 && strings.HasPrefix(t.AppID, "pid:") {
		pid, _ = strconv.Atoi(strings.TrimPrefix(t.AppID, "pid:"))
	}
	var window any
	if t.WindowID != "" {
		n, err := strconv.ParseInt(t.WindowID, 10, 64)
		if err != nil || n <= 0 {
			return ProviderLease{}, failure(ErrTargetNotFound, "无效 Windows windowId")
		}
		window = n
	}
	if pid <= 0 && window == nil && t.Name == "" {
		return ProviderLease{}, failure(ErrTargetNotFound, "Windows 目标需要 PID、windowId 或窗口名称")
	}
	id := t.AppID
	if id == "" {
		if pid > 0 {
			id = fmt.Sprintf("pid:%d", pid)
		} else {
			id = firstString(t.Name, "window:"+t.WindowID)
		}
	}
	return ProviderLease{
		AppID:          id,
		WindowID:       t.WindowID,
		AllowedActions: []Action{ActionClickElement, ActionTypeText, ActionScroll, ActionStop},
		State:          map[string]any{"appId": id, "appName": t.Name, "processId": pid, "windowId": window},
	}, nil
}
func normalizeWindowsSnapshot(m map[string]any, l Lease) (Snapshot, error) {
	shot := object(m["screenshot"])
	data := str(shot["data"])
	if text, ok := m["screenshot"].(string); ok {
		data = text
	}
	if data == "" {
		return Snapshot{}, failure(ErrProviderCrashed, "Windows UIA 没有返回截图")
	}
	display := object(m["display"])
	s := Snapshot{
		AppID:            firstString(m["appId"], l.AppID),
		WindowID:         firstString(m["windowId"], l.WindowID),
		Screenshot:       Image{Type: "image", MIMEType: firstString(shot["mimeType"], "image/png"), Data: data},
		FocusedElementID: str(m["focusedElementId"]),
		Display: Display{
			Bounds: Bounds{
				X:      number(display["x"]),
				Y:      number(display["y"]),
				Width:  number(first(display["width"], shot["width"])),
				Height: number(first(display["height"], shot["height"])),
			},
			ScaleFactor:  number(first(display["scaleFactor"], shot["scaleFactor"])),
			ScreenBounds: boundsOf(display["screenBounds"]),
		},
	}
	if s.Display.ScaleFactor <= 0 {
		s.Display.ScaleFactor = 1
	}
	for _, v := range array(m["elements"]) {
		e := object(v)
		enabled := true
		if b := boolPointer(e["enabled"]); b != nil {
			enabled = *b
		}
		handle, _ := strconv.ParseInt(str(e["nativeWindowHandle"]), 10, 64)
		s.Elements = append(s.Elements, Element{
			ElementID:          str(e["elementId"]),
			Role:               firstString(e["role"], "element"),
			Label:              str(e["label"]),
			Value:              e["value"],
			Enabled:            enabled,
			Bounds:             boundsOf(e["bounds"]),
			Patterns:           stringsOf(e["patterns"]),
			AutomationID:       str(e["automationId"]),
			NativeWindowHandle: handle,
		})
	}
	return s, nil
}
func (p *WindowsProvider) GetAppState(ctx context.Context, l Lease) (Snapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.windows(); err != nil {
		return Snapshot{}, err
	}
	m, err := p.helper(ctx, map[string]any{"command": "get_app_state", "target": l.State})
	if err != nil {
		return Snapshot{}, err
	}
	return normalizeWindowsSnapshot(m, l)
}
func (p *WindowsProvider) PerformAction(ctx context.Context, l Lease, a ResolvedAction) (ActionResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.windows(); err != nil {
		return ActionResult{}, err
	}
	if !contains([]Action{ActionClickElement, ActionTypeText, ActionScroll}, a.Type) || a.ElementID == "" {
		return ActionResult{}, failure(ErrRequiresForeground, "Windows UIA 只允许快照绑定的后台语义动作")
	}
	if a.SnapshotElement == nil || a.SnapshotElement.ElementID != a.ElementID {
		return ActionResult{}, failure(ErrStaleSnapshot, "缺少最新快照的元素元数据")
	}
	m, err := p.helper(ctx, map[string]any{"command": "perform_action", "target": l.State, "action": a})
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{OK: boolean(m["ok"]), Mode: str(m["mode"]), Data: m}, nil
}
func (p *WindowsProvider) ReleaseLease(context.Context, Lease) error { return nil }
func (p *WindowsProvider) Stop(context.Context, Lease) error         { return nil }
func (p *WindowsProvider) Dispose(context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.ownDir {
		err := os.RemoveAll(p.helperDir)
		p.helperDir = ""
		p.ownDir = false
		return err
	}
	return nil
}

// 原生协议使用 UseNumber，防止窗口句柄被浮点转换损失精度。
func decodeNative(data string) (any, error) {
	var v any
	d := json.NewDecoder(strings.NewReader(data))
	d.UseNumber()
	err := d.Decode(&v)
	return v, err
}
func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func array(v any) []any           { a, _ := v.([]any); return a }
func str(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	default:
		return fmt.Sprint(v)
	}
}
func number(v any) float64 { n, _ := strconv.ParseFloat(str(v), 64); return n }
func integer(v any) int    { return int(number(v)) }
func boolean(v any) bool   { b, _ := v.(bool); return b }
func boolPointer(v any) *bool {
	b, ok := v.(bool)
	if !ok {
		return nil
	}
	return &b
}
func first(values ...any) any {
	for _, v := range values {
		if v == nil {
			continue
		}
		if text, ok := v.(string); ok && text == "" {
			continue
		}
		return v
	}
	return nil
}
func firstString(values ...any) string { return str(first(values...)) }
func stringsOf(v any) []string {
	var out []string
	for _, x := range array(v) {
		out = append(out, str(x))
	}
	return out
}
func boundsOf(v any) *Bounds {
	m := object(v)
	if len(m) == 0 {
		return nil
	}
	return &Bounds{X: number(m["x"]), Y: number(m["y"]), Width: number(m["width"]), Height: number(m["height"])}
}
func windowsOf(v any) []Window {
	out := []Window{}
	for _, x := range array(v) {
		m := object(x)
		id := firstString(m["window_id"], m["windowId"], m["id"], m["nativeWindowHandle"])
		if id == "" {
			continue
		}
		out = append(out, Window{
			WindowID:       id,
			Title:          firstString(m["title"], m["name"]),
			Bounds:         boundsOf(m["bounds"]),
			IsOnScreen:     boolPointer(first(m["is_on_screen"], m["isOnScreen"])),
			OnCurrentSpace: boolPointer(first(m["on_current_space"], m["onCurrentSpace"])),
		})
	}
	return out
}
func appsOf(v any) []App {
	items := array(v)
	if items == nil {
		m := object(v)
		items = array(first(m["apps"], m["items"]))
	}
	out := []App{}
	for _, x := range items {
		m := object(x)
		pid := integer(first(m["pid"], m["process_id"], m["processId"]))
		id := firstString(m["bundle_id"], m["bundleId"], m["appId"], m["id"])
		if id == "" && pid > 0 {
			id = fmt.Sprintf("pid:%d", pid)
		}
		if id == "" {
			continue
		}
		out = append(out, App{
			AppID:        id,
			Name:         firstString(m["name"], m["localized_name"], m["displayName"], id),
			PID:          pid,
			Active:       boolean(first(m["active"], m["is_active"])),
			Windows:      windowsOf(m["windows"]),
			ProviderData: map[string]any{"bundleId": id, "pid": pid},
		})
	}
	return out
}

// CommandRunner 可注入测试替身；参数独立传递，禁止经 shell 拼接执行。
type Command struct {
	Name           string
	Args           []string
	Env            map[string]string
	Timeout        time.Duration
	MaxOutputBytes int
}
type CommandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}
type ManagedProcess interface {
	Kill() error
	Done() <-chan struct{}
}
type CommandRunner interface {
	Run(context.Context, Command) (CommandResult, error)
	Start(Command) (ManagedProcess, error)
}
type ExecRunner struct{}

type boundedBuffer struct {
	// 不嵌入 bytes.Buffer，避免 io.Copy 调用其 ReaderFrom 绕过下面的容量检查。
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.buffer.Len()
	if remaining < 0 {
		remaining = 0
	}
	if len(p) > remaining {
		p = p[:remaining]
		b.overflow = true
	}
	_, _ = b.buffer.Write(p)
	return n, nil
}

func (b *boundedBuffer) String() string { return b.buffer.String() }
func makeCommand(c Command) *exec.Cmd {
	cmd := exec.Command(c.Name, c.Args...)
	env := map[string]string{}
	for _, entry := range os.Environ() {
		k, v, ok := strings.Cut(entry, "=")
		if ok {
			env[k] = v
		}
	}
	for k, v := range c.Env {
		env[k] = v
	}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	configureProcess(cmd)
	return cmd
}
func (ExecRunner) Run(ctx context.Context, c Command) (CommandResult, error) {
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return CommandResult{}, err
	}
	cmd := makeCommand(c)
	limit := c.MaxOutputBytes
	if limit <= 0 {
		limit = 32 << 20
	}
	out := &boundedBuffer{limit: limit}
	stderr := &boundedBuffer{limit: 64 << 10}
	cmd.Stdout = out
	cmd.Stderr = stderr
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return CommandResult{}, err
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = killProcess(cmd)
		case <-done:
		}
	}()
	err := cmd.Wait()
	close(done)
	result := CommandResult{Stdout: out.String(), Stderr: stderr.String(), ExitCode: cmd.ProcessState.ExitCode()}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if out.overflow || stderr.overflow {
		return result, fmt.Errorf("helper 输出超过大小限制")
	}
	var exitErr *exec.ExitError
	if err != nil && !asExitError(err, &exitErr) {
		return result, err
	}
	return result, nil
}
func asExitError(err error, target **exec.ExitError) bool {
	v, ok := err.(*exec.ExitError)
	if ok {
		*target = v
	}
	return ok
}

type execProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
	once sync.Once
}

func (p *execProcess) Kill() error {
	var err error
	p.once.Do(func() { err = killProcess(p.cmd) })
	return err
}
func (p *execProcess) Done() <-chan struct{} { return p.done }
func (ExecRunner) Start(c Command) (ManagedProcess, error) {
	cmd := makeCommand(c)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &execProcess{cmd: cmd, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(p.done) }()
	return p, nil
}
