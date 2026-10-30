package computeruse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MacOSConfig 支持原项目的 HANA_* 环境变量和显式 helper 路径。
type MacOSConfig struct {
	Command             string
	SocketPath          string
	Platform            string
	Runner              CommandRunner
	Env                 map[string]string
	Timeout             time.Duration
	StartupTimeout      time.Duration
	AutoStartDaemon     *bool
	DisableCursor       bool
	CursorStyle         map[string]any
	CursorMotion        map[string]any
	LaunchRetryAttempts int
	LaunchRetryDelay    time.Duration
	RootDir             string
}
type MacOSProvider struct {
	mu               sync.Mutex
	config           MacOSConfig
	runner           CommandRunner
	bundled          bool
	autoStart        bool
	process          ManagedProcess
	cursorConfigured bool
	socketDir        string
}

func NewMacOSProvider(config *MacOSConfig) *MacOSProvider {
	c := MacOSConfig{}
	if config != nil {
		c = *config
	}
	if c.Platform == "" {
		c.Platform = runtime.GOOS
	}
	c.Env = clone(c.Env)
	if c.Timeout <= 0 {
		c.Timeout = 30 * time.Second
	}
	if c.StartupTimeout <= 0 {
		c.StartupTimeout = 5 * time.Second
	}
	if c.LaunchRetryAttempts <= 0 {
		c.LaunchRetryAttempts = 3
	}
	if c.LaunchRetryDelay <= 0 {
		c.LaunchRetryDelay = 350 * time.Millisecond
	}
	if c.Command == "" {
		c.Command = ResolveCUADriverCommand(c)
	}
	if c.Runner == nil {
		c.Runner = ExecRunner{}
	}
	p := &MacOSProvider{config: c, runner: c.Runner, bundled: filepath.Base(c.Command) == "hana-computer-use-helper"}
	p.autoStart = p.bundled
	if c.AutoStartDaemon != nil {
		p.autoStart = *c.AutoStartDaemon
	}
	if c.SocketPath == "" {
		c.SocketPath = envValue(c.Env, "HANA_COMPUTER_USE_SOCKET_PATH")
	}
	if c.SocketPath == "" {
		dir := filepath.Join(os.TempDir(), "eino-cu-"+newID()[:12])
		if len(dir)+12 > 100 {
			dir = filepath.Join("/tmp", "eino-cu-"+newID()[:12])
		}
		p.socketDir = dir
		c.SocketPath = filepath.Join(dir, "helper.sock")
	}
	p.config = c
	return p
}
func envValue(env map[string]string, key string) string {
	if v, ok := env[key]; ok {
		return v
	}
	return os.Getenv(key)
}
func expandHome(p string) string {
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
}
func appResources(p string) string {
	p = filepath.Clean(p)
	if !filepath.IsAbs(p) || filepath.Base(p) != "Resources" {
		return ""
	}
	contents := filepath.Dir(p)
	if filepath.Base(contents) == "Contents" && strings.HasSuffix(filepath.Dir(contents), ".app") {
		return p
	}
	return ""
}

// ResolveCUADriverCommand 保留打包路径的权威性，不因安装损坏而切换到未知驱动。
func ResolveCUADriverCommand(c MacOSConfig) string {
	if p := envValue(c.Env, "HANA_COMPUTER_USE_HELPER_PATH"); p != "" {
		return expandHome(p)
	}
	if p := envValue(c.Env, "HANA_COMPUTER_USE_RUNTIME_ROOT"); p != "" {
		return filepath.Join(expandHome(p), "hana-computer-use-helper")
	}
	roots := []string{}
	if envValue(c.Env, "HANA_DESKTOP_IS_PACKAGED") == "1" {
		roots = append(roots, appResources(envValue(c.Env, "HANA_DESKTOP_RESOURCES_PATH")))
		if p := envValue(c.Env, "HANA_DESKTOP_APP_PATH"); filepath.Base(p) == "app.asar" {
			roots = append(roots, appResources(filepath.Dir(p)))
		}
		if p := envValue(c.Env, "HANA_DESKTOP_EXEC_PATH"); filepath.Base(filepath.Dir(p)) == "MacOS" {
			roots = append(roots, appResources(filepath.Join(filepath.Dir(filepath.Dir(p)), "Resources")))
		}
	}
	if root := envValue(c.Env, "HANA_ROOT"); filepath.Base(root) == "server" {
		roots = append(roots, appResources(filepath.Dir(root)))
	}
	var packaged []string
	for _, r := range roots {
		if r != "" {
			packaged = append(packaged, filepath.Join(r, "computer-use", "macos", "hana-computer-use-helper"))
		}
	}
	for _, p := range packaged {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	if len(packaged) > 0 {
		return packaged[0]
	}
	var candidates []string
	if envValue(c.Env, "HANA_DESKTOP_IS_PACKAGED") != "1" {
		root := c.RootDir
		if root == "" {
			root = envValue(c.Env, "HANA_ROOT")
		}
		cwd, _ := os.Getwd()
		arch := runtime.GOARCH
		if arch == "amd64" {
			arch = "x64"
		}
		for _, r := range []string{root, cwd} {
			if r != "" {
				candidates = append(candidates, filepath.Join(r, "dist-computer-use", "mac-"+arch, "hana-computer-use-helper"))
			}
		}
	}
	candidates = append(candidates, envValue(c.Env, "HANA_CUA_DRIVER_PATH"), "~/.local/bin/cua-driver", "/usr/local/bin/cua-driver", "/Applications/CuaDriver.app/Contents/MacOS/cua-driver")
	for _, candidate := range candidates {
		candidate = expandHome(candidate)
		if candidate != "" {
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		}
	}
	return "cua-driver"
}
func (p *MacOSProvider) ID() string { return "macos:cua" }
func (p *MacOSProvider) Capabilities() Capabilities {
	return Capabilities{
		Platform:          "macos",
		ObservationModes:  []string{"vision-native"},
		Screenshot:        true,
		AccessibilityTree: true,
		ElementActions:    true,
		BackgroundControl: "full",
		PointClick:        "unsupported",
		Drag:              "unsupported",
		TextInput:         "semantic",
		KeyboardInput:     "pidScoped",
		NativeCursor:      p.bundled && !p.config.DisableCursor,
	}
}
func (p *MacOSProvider) darwin() error {
	if p.config.Platform != "darwin" {
		return failure(ErrProviderUnavailable, "macOS Cua 仅在 macOS 可用")
	}
	return nil
}
func (p *MacOSProvider) environment() map[string]string {
	env := clone(p.config.Env)
	if env == nil {
		env = map[string]string{}
	}
	env["HANA_COMPUTER_USE_SOCKET_PATH"] = p.config.SocketPath
	if p.bundled {
		style := p.config.CursorStyle
		if style == nil {
			style = map[string]any{"gradient_colors": []string{"#FFFDF8", "#8FAABD", "#2F4A56"}, "bloom_color": "#537D96", "image_path": ""}
		}
		motion := p.config.CursorMotion
		if motion == nil {
			motion = map[string]any{
				"start_handle":         0.38,
				"end_handle":           0.28,
				"arc_size":             0.08,
				"spring":               1,
				"glide_duration_ms":    520,
				"dwell_after_click_ms": 160,
				"idle_hide_ms":         2600,
			}
		}
		b, _ := json.Marshal(map[string]any{"enabled": !p.config.DisableCursor, "style": style, "motion": motion})
		env["HANA_AGENT_CURSOR_CONFIG_JSON"] = string(b)
	}
	return env
}
func (p *MacOSProvider) run(ctx context.Context, args []string, timeout time.Duration) (CommandResult, error) {
	return p.runner.Run(ctx, Command{Name: p.config.Command, Args: args, Env: p.environment(), Timeout: timeout})
}

type cuaResult struct {
	content    []any
	structured any
}

func (r cuaResult) text() string {
	var texts []string
	for _, v := range r.content {
		m := object(v)
		if str(m["type"]) == "text" {
			texts = append(texts, str(m["text"]))
		}
	}
	return strings.Join(texts, "\n")
}
func (p *MacOSProvider) tool(ctx context.Context, name string, payload any) (cuaResult, error) {
	args := []string{name}
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return cuaResult{}, err
		}
		args = append(args, string(b))
	}
	args = append(args, "--raw", "--compact", "--socket", p.config.SocketPath)
	r, err := p.run(ctx, args, p.config.Timeout)
	if err != nil {
		// 终止 CLI 不代表 daemon 中的动作已经停止；重置自有服务以隔离后续请求。
		if p.process != nil && (ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded)) {
			_ = p.stop(ctx)
		}
		if ctx.Err() != nil {
			return cuaResult{}, ctx.Err()
		}
		if errors.Is(err, context.DeadlineExceeded) {
			return cuaResult{}, err
		}
		code := ErrProviderCrashed
		if errors.Is(err, os.ErrNotExist) {
			code = ErrProviderUnavailable
		}
		return cuaResult{}, &ComputerUseError{Code: code, Message: "无法执行 Cua helper: " + err.Error(), Cause: err}
	}
	value, parseErr := decodeNative(r.Stdout)
	m := object(value)
	result := cuaResult{content: array(m["content"]), structured: first(m["structuredContent"], m["structured_content"], m["data"])}
	if r.ExitCode != 0 || boolean(m["isError"]) {
		message := firstString(result.text(), r.Stderr, "Cua helper 执行失败")
		code := ErrProviderCrashed
		lower := strings.ToLower(message)
		for _, s := range []string{"permission", "accessibility", "screen recording", "tcc"} {
			if strings.Contains(lower, s) {
				code = ErrOSPermissionDenied
				break
			}
		}
		return cuaResult{}, failure(code, message)
	}
	if parseErr != nil {
		return cuaResult{}, failure(ErrProviderCrashed, "Cua helper 返回无效 JSON")
	}
	return result, nil
}
func (p *MacOSProvider) ensureDaemon(ctx context.Context) error {
	if !p.autoStart {
		return nil
	}
	if p.process != nil {
		select {
		case <-p.process.Done():
			p.process = nil
			p.cursorConfigured = false
		default:
		}
	}
	r, err := p.run(ctx, []string{"status", "--socket", p.config.SocketPath}, 2*time.Second)
	if err == nil && r.ExitCode == 0 {
		return nil
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if p.socketDir != "" {
		if err = os.MkdirAll(p.socketDir, 0700); err != nil {
			return err
		}
	}
	process, err := p.runner.Start(Command{Name: p.config.Command, Args: []string{"serve", "--socket", p.config.SocketPath}, Env: p.environment()})
	if err != nil {
		return &ComputerUseError{Code: ErrProviderUnavailable, Message: "无法启动 Cua daemon: " + err.Error(), Cause: err}
	}
	p.process = process
	ready, cancel := context.WithTimeout(ctx, p.config.StartupTimeout)
	defer cancel()
	for {
		r, e := p.run(ready, []string{"status", "--socket", p.config.SocketPath}, 2*time.Second)
		if e == nil && r.ExitCode == 0 {
			return nil
		}
		select {
		case <-ready.Done():
			_ = process.Kill()
			p.process = nil
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return failure(ErrProviderUnavailable, "Cua daemon 启动超时")
		case <-process.Done():
			p.process = nil
			return failure(ErrProviderUnavailable, "Cua daemon 启动后立即退出")
		case <-time.After(120 * time.Millisecond):
		}
	}
}
func (p *MacOSProvider) cursor(ctx context.Context) error {
	if !p.bundled || p.cursorConfigured {
		return nil
	}
	style := p.config.CursorStyle
	if style != nil {
		if _, err := p.tool(ctx, "set_agent_cursor_style", style); err != nil {
			return err
		}
	}
	if p.config.CursorMotion != nil {
		if _, err := p.tool(ctx, "set_agent_cursor_motion", p.config.CursorMotion); err != nil {
			return err
		}
	}
	if _, err := p.tool(ctx, "set_agent_cursor_enabled", map[string]any{"enabled": !p.config.DisableCursor}); err != nil {
		return err
	}
	p.cursorConfigured = true
	return nil
}
func normalizePermissions(r cuaResult) []Permission {
	m := object(r.structured)
	var out []Permission
	if list := array(m["permissions"]); list != nil {
		for _, v := range list {
			x := object(v)
			out = append(out, Permission{Name: str(x["name"]), Granted: boolean(x["granted"])})
		}
		return out
	}
	for _, line := range strings.Split(r.text(), "\n") {
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		lower := strings.ToLower(value)
		granted := true
		for _, term := range []string{"not granted", "missing", "denied", "false"} {
			if strings.Contains(lower, term) {
				granted = false
			}
		}
		name = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(name), "✅❌"))
		out = append(out, Permission{Name: name, Granted: granted})
	}
	return out
}
func (p *MacOSProvider) GetStatus(ctx context.Context) (ProviderStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.config.Platform != "darwin" {
		return ProviderStatus{Reason: "unsupported-platform"}, nil
	}
	r, err := p.run(ctx, []string{"status", "--socket", p.config.SocketPath}, 5*time.Second)
	if err != nil {
		if ctx.Err() != nil {
			return ProviderStatus{}, ctx.Err()
		}
		return ProviderStatus{
			Reason:  "binary-or-daemon-unavailable",
			Details: map[string]any{"command": p.config.Command, "error": err.Error()},
		}, nil
	}
	if r.ExitCode != 0 {
		return ProviderStatus{Reason: "daemon-unavailable"}, nil
	}
	perms, err := p.tool(ctx, "check_permissions", map[string]any{"prompt": false})
	if err != nil {
		return ProviderStatus{Available: true, Reason: err.Error()}, nil
	}
	return ProviderStatus{Available: true, Permissions: normalizePermissions(perms)}, nil
}
func (p *MacOSProvider) RequestPermissions(ctx context.Context) (ProviderStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.darwin(); err != nil {
		return ProviderStatus{}, err
	}
	if err := p.ensureDaemon(ctx); err != nil {
		return ProviderStatus{}, err
	}
	r, err := p.tool(ctx, "check_permissions", map[string]any{"prompt": true})
	if err != nil {
		return ProviderStatus{}, err
	}
	return ProviderStatus{Available: true, Permissions: normalizePermissions(r)}, nil
}
func (p *MacOSProvider) ListApps(ctx context.Context) ([]App, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.darwin(); err != nil {
		return nil, err
	}
	if err := p.ensureDaemon(ctx); err != nil {
		return nil, err
	}
	r, err := p.tool(ctx, "list_apps", nil)
	if err != nil {
		return nil, err
	}
	return appsOf(r.structured), nil
}
func selectWindow(windows []Window, id string) *Window {
	best := -1
	bestScore := -1.0
	for i, w := range windows {
		if id != "" && w.WindowID == id {
			return &windows[i]
		}
		score := 0.0
		if w.IsOnScreen != nil && *w.IsOnScreen {
			score += 1000
		}
		if w.OnCurrentSpace != nil && *w.OnCurrentSpace {
			score += 500
		}
		if w.Title != "" {
			score += 100
		}
		if w.Bounds != nil {
			score += min(80, w.Bounds.Width*w.Bounds.Height/10000)
			if w.Bounds.Height > 80 {
				score += 50
			}
		}
		if score > bestScore {
			best = i
			bestScore = score
		}
	}
	if id != "" {
		return nil
	}
	if best < 0 {
		return nil
	}
	return &windows[best]
}
func macLease(a App, windows []Window, t Target) (ProviderLease, error) {
	w := selectWindow(windows, t.WindowID)
	if a.PID <= 0 || w == nil {
		return ProviderLease{}, failure(ErrTargetNotFound, "目标应用还没有可用窗口")
	}
	pid, e1 := strconv.Atoi(str(a.PID))
	wid, e2 := strconv.Atoi(w.WindowID)
	if e1 != nil || e2 != nil || pid <= 0 || wid <= 0 {
		return ProviderLease{}, failure(ErrTargetNotFound, "无效 PID 或 windowId")
	}
	return ProviderLease{
		AppID:          a.AppID,
		WindowID:       w.WindowID,
		AllowedActions: []Action{ActionClickElement, ActionTypeText, ActionPressKey, ActionScroll, ActionSecondary, ActionStop},
		State:          map[string]any{"pid": pid, "windowId": wid, "bundleId": a.AppID, "appName": a.Name},
	}, nil
}
func (p *MacOSProvider) CreateLease(ctx context.Context, t Target) (ProviderLease, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.darwin(); err != nil {
		return ProviderLease{}, err
	}
	if err := p.ensureDaemon(ctx); err != nil {
		return ProviderLease{}, err
	}
	if err := p.cursor(ctx); err != nil {
		return ProviderLease{}, err
	}
	if t.PID > 0 {
		if _, err := strconv.Atoi(t.WindowID); err != nil {
			return ProviderLease{}, failure(ErrTargetNotFound, "PID 目标必须带有效 windowId")
		}
		id := t.AppID
		if id == "" {
			id = fmt.Sprintf("pid:%d", t.PID)
		}
		return macLease(App{AppID: id, Name: t.Name, PID: t.PID}, []Window{{WindowID: t.WindowID}}, t)
	}
	r, err := p.tool(ctx, "list_apps", nil)
	if err != nil {
		return ProviderLease{}, err
	}
	for _, a := range appsOf(r.structured) {
		if (t.AppID != "" && strings.EqualFold(a.AppID, t.AppID)) || (t.AppID == "" && strings.EqualFold(a.Name, t.Name)) {
			wins, e := p.tool(ctx, "list_windows", map[string]any{"pid": a.PID})
			if e != nil {
				return ProviderLease{}, e
			}
			v := wins.structured
			if m := object(v); m != nil {
				v = first(m["windows"], m["items"])
			}
			if l, e := macLease(a, windowsOf(v), t); e == nil {
				return l, nil
			}
		}
	}
	payload := map[string]any{"name": t.Name}
	if t.AppID != "" {
		payload = map[string]any{"bundle_id": t.AppID}
	}
	for attempt := 0; attempt < p.config.LaunchRetryAttempts; attempt++ {
		r, err = p.tool(ctx, "launch_app", payload)
		if err != nil {
			return ProviderLease{}, err
		}
		m := object(r.structured)
		a := App{
			AppID: firstString(m["bundle_id"], m["bundleId"], t.AppID),
			Name:  firstString(m["name"], t.Name),
			PID:   integer(first(m["pid"], m["process_id"])),
		}
		if a.AppID == "" {
			a.AppID = fmt.Sprintf("pid:%d", a.PID)
		}
		l, e := macLease(a, windowsOf(m["windows"]), t)
		if e == nil {
			return l, nil
		}
		err = e
		if attempt+1 < p.config.LaunchRetryAttempts {
			select {
			case <-ctx.Done():
				return ProviderLease{}, ctx.Err()
			case <-time.After(p.config.LaunchRetryDelay):
			}
		}
	}
	return ProviderLease{}, err
}

var treeRow = regexp.MustCompile(`^(\s*)-\s+\[(\d+)\]\s+([A-Za-z0-9_]+)(.*)$`)
var quotedLabel = regexp.MustCompile(`"([^"]+)"`)
var actionsLabel = regexp.MustCompile(`actions=\[([^\]]*)\]`)
var treeTitle = regexp.MustCompile(`^\s*"([^"]*)"`)
var treeValue = regexp.MustCompile(`(?:^|\s)=\s*"([^"]*)"`)
var treeDescription = regexp.MustCompile(`\(([^)]*)\)`)
var treeIdentifier = regexp.MustCompile(`(?:^|\s)id=([^\s]+)`)

// Cua 的 Markdown 把 AXTitle、AXValue 和 AXDescription 分别放在
// 引号、等号引号和括号中。将标签与值分开，避免输入后丢失可识别的标签。
func parseTreeAttributes(fragment string) (label string, value any, identifier string) {
	rest := fragment
	if match := treeTitle.FindStringSubmatchIndex(rest); match != nil {
		label = rest[match[2]:match[3]]
		rest = rest[match[1]:]
	}
	if match := treeValue.FindStringSubmatchIndex(rest); match != nil {
		value = rest[match[2]:match[3]]
		rest = rest[:match[0]] + rest[match[1]:]
	}
	if match := treeDescription.FindStringSubmatch(rest); len(match) > 1 && label == "" {
		label = match[1]
	}
	if match := treeIdentifier.FindStringSubmatch(rest); len(match) > 1 {
		identifier = match[1]
	}
	return label, value, identifier
}

func parseTree(markdown string) []Element {
	var out []Element
	lines := strings.Split(markdown, "\n")
	for i, line := range lines {
		m := treeRow.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		label, value, identifier := parseTreeAttributes(m[4])
		if label == "" {
			var labels []string
			for _, child := range lines[i+1:] {
				indent := len(child) - len(strings.TrimLeft(child, " \t"))
				if indent <= len(m[1]) && strings.HasPrefix(strings.TrimSpace(child), "- ") {
					break
				}
				if q := quotedLabel.FindStringSubmatch(child); len(q) > 1 && !contains(labels, q[1]) {
					labels = append(labels, q[1])
				}
			}
			label = strings.Join(labels, " ")
		}
		var actions []string
		if a := actionsLabel.FindStringSubmatch(m[4]); len(a) > 1 {
			for _, action := range strings.Split(a[1], ",") {
				if v := strings.TrimSpace(action); v != "" {
					actions = append(actions, v)
				}
			}
		}
		out = append(out, Element{ElementID: m[2], Role: m[3], Label: label, Value: value, AutomationID: identifier, Enabled: !strings.Contains(m[4], "DISABLED"), Actions: actions})
	}
	return out
}
func normalizeCuaSnapshot(r cuaResult, l Lease) (Snapshot, error) {
	m := object(r.structured)
	snapshot := Snapshot{AppID: l.AppID, WindowID: l.WindowID, FocusedElementID: str(m["focusedElementId"])}
	for _, v := range r.content {
		b := object(v)
		if str(b["type"]) == "image" {
			snapshot.Screenshot = Image{Type: "image", MIMEType: firstString(b["mimeType"], b["mime_type"], "image/png"), Data: str(b["data"])}
			break
		}
	}
	if snapshot.Screenshot.Data == "" {
		return Snapshot{}, failure(ErrProviderCrashed, "Cua 没有返回截图；请检查 capture_mode=som 和屏幕录制权限")
	}
	d := object(first(m["display"], m["screen"]))
	snapshot.Display = Display{
		Bounds: Bounds{
			X:      number(d["x"]),
			Y:      number(d["y"]),
			Width:  number(first(m["screenshot_width"], d["width"])),
			Height: number(first(m["screenshot_height"], d["height"])),
		},
		OriginalWidth:  number(first(m["screenshot_original_width"], d["originalWidth"])),
		OriginalHeight: number(first(m["screenshot_original_height"], d["originalHeight"])),
		ScaleFactor:    number(first(m["screenshot_scale_factor"], d["scaleFactor"])),
	}
	if snapshot.Display.ScaleFactor <= 0 {
		snapshot.Display.ScaleFactor = 1
	}
	if elements, ok := m["elements"].([]any); ok {
		for i, v := range elements {
			e := object(v)
			enabled := true
			if b := boolPointer(e["enabled"]); b != nil {
				enabled = *b
			}
			snapshot.Elements = append(snapshot.Elements, Element{
				ElementID: firstString(e["element_index"], e["elementId"], e["id"], i),
				Role:      firstString(e["role"], e["ax_role"], e["type"], "element"),
				Label:     firstString(e["label"], e["name"], e["title"]),
				Value:     e["value"],
				Enabled:   enabled,
				Actions:   stringsOf(e["actions"]),
				Bounds:    boundsOf(e["bounds"]),
			})
		}
	} else {
		snapshot.Elements = parseTree(firstString(m["tree_markdown"], m["treeMarkdown"], r.text()))
	}
	return snapshot, nil
}
func (p *MacOSProvider) GetAppState(ctx context.Context, l Lease) (Snapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.darwin(); err != nil {
		return Snapshot{}, err
	}
	if err := p.ensureDaemon(ctx); err != nil {
		return Snapshot{}, err
	}
	if err := p.cursor(ctx); err != nil {
		return Snapshot{}, err
	}
	pid, wid := integer(l.State["pid"]), integer(l.State["windowId"])
	if pid <= 0 || wid <= 0 {
		return Snapshot{}, failure(ErrTargetNotFound, "租约缺少 PID/windowId")
	}
	r, err := p.tool(ctx, "get_window_state", map[string]any{"pid": pid, "window_id": wid})
	if err != nil {
		return Snapshot{}, err
	}
	return normalizeCuaSnapshot(r, l)
}
func (p *MacOSProvider) PerformAction(ctx context.Context, l Lease, a ResolvedAction) (ActionResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.darwin(); err != nil {
		return ActionResult{}, err
	}
	if err := p.ensureDaemon(ctx); err != nil {
		return ActionResult{}, err
	}
	if err := p.cursor(ctx); err != nil {
		return ActionResult{}, err
	}
	pid, wid := integer(l.State["pid"]), integer(l.State["windowId"])
	if pid <= 0 || wid <= 0 {
		return ActionResult{}, failure(ErrTargetNotFound, "租约缺少 PID/windowId")
	}
	payload := map[string]any{"pid": pid}
	if a.ElementID != "" {
		index, err := strconv.Atoi(strings.TrimPrefix(a.ElementID, "cua:"))
		if err != nil || index < 0 {
			return ActionResult{}, failure(ErrTargetNotFound, "无效 Cua 元素编号")
		}
		if a.SnapshotElement == nil {
			return ActionResult{}, failure(ErrStaleSnapshot, "缺少快照元素")
		}
		payload["window_id"] = wid
		payload["element_index"] = index
	}
	name := ""
	switch a.Type {
	case ActionClickElement, ActionSecondary:
		if a.ElementID == "" {
			return ActionResult{}, failure(ErrTargetNotFound, "元素动作需要 elementId")
		}
		name = "click"
		if a.Type == ActionSecondary {
			name = "right_click"
		} else if actions := a.SnapshotElement.Actions; len(actions) > 0 && !contains(actions, "AXPress") {
			if contains(actions, "AXShowDefaultUI") {
				payload["action"] = "show_default_ui"
			} else if contains(actions, "AXOpen") {
				payload["action"] = "open"
			}
		}
	case ActionTypeText:
		name = "type_text"
		payload["text"] = a.Text
	case ActionPressKey:
		name = "press_key"
		payload = map[string]any{"pid": pid, "key": a.Key}
	case ActionScroll:
		name = "scroll"
		payload["direction"] = a.Direction
		amount := a.Amount
		if amount <= 0 {
			amount = 3
		}
		payload["amount"] = amount
	default:
		return ActionResult{}, failure(ErrCapabilityUnsupported, "macOS 不支持此动作，像素输入已禁用")
	}
	r, err := p.tool(ctx, name, payload)
	if err != nil {
		return ActionResult{}, err
	}
	return ActionResult{OK: true, Mode: "background", Output: r.text(), Data: object(r.structured)}, nil
}

// 只停止本 Provider 启动的 daemon，避免关闭用户或其他工具正在使用的服务。
func (p *MacOSProvider) stop(ctx context.Context) error {
	if p.process == nil {
		return nil
	}
	process := p.process
	p.process = nil
	p.cursorConfigured = false
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	r, err := p.run(cleanup, []string{"stop", "--socket", p.config.SocketPath}, 4*time.Second)
	if err != nil || r.ExitCode != 0 {
		_ = process.Kill()
	}
	select {
	case <-process.Done():
	case <-cleanup.Done():
		_ = process.Kill()
	}
	return nil
}
func (p *MacOSProvider) Stop(ctx context.Context, _ Lease) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stop(ctx)
}
func (p *MacOSProvider) ReleaseLease(ctx context.Context, l Lease) error { return p.Stop(ctx, l) }
func (p *MacOSProvider) Dispose(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	err := p.stop(ctx)
	if p.socketDir != "" {
		_ = os.Remove(filepath.Join(p.socketDir, "helper.sock"))
		_ = os.Remove(p.socketDir)
	}
	return err
}
