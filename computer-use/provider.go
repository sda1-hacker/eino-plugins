package computeruse

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"math"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// MacOSConfig 配置进程内的 macOS 原生后端，不支持外部执行程序。
type MacOSConfig struct {
	// Platform 默认是 runtime.GOOS，主要用于宿主环境检查和测试。
	Platform string
	// Timeout 限制单次系统操作时间，默认 30 秒。
	Timeout time.Duration
}

// MacOSProvider 直接调用系统 API；元素缓存属于当前实例，不创建子进程或 socket。
type MacOSProvider struct {
	mu     sync.Mutex
	config MacOSConfig
	driver nativeMacOSDriver
}

// NewMacOSProvider 创建纯 Go 后端；传入 nil 使用默认配置。
func NewMacOSProvider(config *MacOSConfig) *MacOSProvider {
	c := MacOSConfig{}
	if config != nil {
		c = *config
	}
	if c.Platform == "" {
		c.Platform = runtime.GOOS
	}
	if c.Timeout <= 0 {
		c.Timeout = 30 * time.Second
	}
	return &MacOSProvider{config: c}
}

func (p *MacOSProvider) ID() string { return "macos:ax" }
func (p *MacOSProvider) Capabilities() Capabilities {
	return Capabilities{
		Platform: "macos", ObservationModes: []string{"vision-native"},
		Screenshot: true, AccessibilityTree: true, ElementActions: true,
		BackgroundControl: "partial", PointClick: "unsupported", Drag: "unsupported",
		TextInput: "semantic", KeyboardInput: "pidScoped",
	}
}

// 每次系统调用都在 Provider 锁内执行，防止其他观察替换当前元素缓存。
func (p *MacOSProvider) getDriver(ctx context.Context) (nativeMacOSDriver, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p.config.Platform != "darwin" {
		return nil, failure(ErrProviderUnavailable, "macOS 原生后端仅在 macOS 可用")
	}
	return p.nativeDriver()
}
func (p *MacOSProvider) GetStatus(ctx context.Context) (ProviderStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return ProviderStatus{}, err
	}
	if p.config.Platform != "darwin" {
		return ProviderStatus{Reason: "unsupported-platform"}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, p.config.Timeout)
	defer cancel()
	d, err := p.getDriver(ctx)
	if err != nil {
		return ProviderStatus{}, err
	}
	return d.Status(ctx, false)
}
func (p *MacOSProvider) RequestPermissions(ctx context.Context) (ProviderStatus, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, p.config.Timeout)
	defer cancel()
	d, err := p.getDriver(ctx)
	if err != nil {
		return ProviderStatus{}, err
	}
	return d.Status(ctx, true)
}
func (p *MacOSProvider) ListApps(ctx context.Context) ([]App, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, p.config.Timeout)
	defer cancel()
	d, err := p.getDriver(ctx)
	if err != nil {
		return nil, err
	}
	return d.ListApps(ctx)
}
func (p *MacOSProvider) CreateLease(ctx context.Context, target Target) (ProviderLease, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, p.config.Timeout)
	defer cancel()
	d, err := p.getDriver(ctx)
	if err != nil {
		return ProviderLease{}, err
	}
	return d.CreateLease(ctx, target)
}
func (p *MacOSProvider) GetAppState(ctx context.Context, lease Lease) (Snapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, p.config.Timeout)
	defer cancel()
	d, err := p.getDriver(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	return d.Observe(ctx, lease)
}
func (p *MacOSProvider) PerformAction(ctx context.Context, lease Lease, action ResolvedAction) (ActionResult, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, p.config.Timeout)
	defer cancel()
	d, err := p.getDriver(ctx)
	if err != nil {
		return ActionResult{}, err
	}
	return d.Act(ctx, lease, action)
}

// Stop 仅释放本进程持有的系统对象，不退出用户的目标应用。
func (p *MacOSProvider) Stop(context.Context, Lease) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.driver == nil {
		return nil
	}
	return p.driver.Close()
}
func (p *MacOSProvider) ReleaseLease(ctx context.Context, lease Lease) error {
	return p.Stop(ctx, lease)
}
func (p *MacOSProvider) Dispose(ctx context.Context) error { return p.Stop(ctx, Lease{}) }

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

// nativeMacOSDriver 在当前 Go 进程中直接调用系统 API，不使用外部执行程序。
type nativeMacOSDriver interface {
	Status(context.Context, bool) (ProviderStatus, error)
	ListApps(context.Context) ([]App, error)
	CreateLease(context.Context, Target) (ProviderLease, error)
	Observe(context.Context, Lease) (Snapshot, error)
	Act(context.Context, Lease, ResolvedAction) (ActionResult, error)
	Close() error
}

func (p *MacOSProvider) nativeDriver() (nativeMacOSDriver, error) {
	if p.driver == nil {
		driver, err := newNativeMacOSDriver()
		if err != nil {
			return nil, err
		}
		p.driver = driver
	}
	return p.driver, nil
}

// matchMacWindow 使用公开 AX 属性匹配窗口；重叠且无法消歧时拒绝猜测。
func matchMacWindow(candidates []Window, target Window) int {
	match, titled, count, titleCount := -1, -1, 0, 0
	for i, candidate := range candidates {
		a, b := candidate.Bounds, target.Bounds
		if a == nil || b == nil || math.Abs(a.X-b.X) >= 6 || math.Abs(a.Y-b.Y) >= 6 || math.Abs(a.Width-b.Width) >= 6 || math.Abs(a.Height-b.Height) >= 6 {
			continue
		}
		match, count = i, count+1
		if target.Title != "" && candidate.Title == target.Title {
			titled, titleCount = i, titleCount+1
		}
	}
	if titleCount == 1 {
		return titled
	}
	if count == 1 {
		return match
	}
	return -1
}

// macKeyCode 只生成目标 PID 的公开 CGEvent 按键；不使用系统私有认证接口。
func macKeyCode(key string) (uint16, uint64, error) {
	parts := strings.Split(strings.ToLower(strings.TrimSpace(key)), "+")
	var flags uint64
	for _, part := range parts[:len(parts)-1] {
		switch part {
		case "cmd", "command":
			flags |= 1 << 20
		case "shift":
			flags |= 1 << 17
		case "alt", "option":
			flags |= 1 << 19
		case "ctrl", "control":
			flags |= 1 << 18
		default:
			return 0, 0, failure(ErrInvalidArgument, "未知按键修饰符："+part)
		}
	}
	codes := map[string]uint16{"a": 0, "s": 1, "d": 2, "f": 3, "h": 4, "g": 5, "z": 6, "x": 7, "c": 8, "v": 9, "b": 11, "q": 12, "w": 13, "e": 14, "r": 15, "y": 16, "t": 17, "1": 18, "2": 19, "3": 20, "4": 21, "6": 22, "5": 23, "=": 24, "9": 25, "7": 26, "-": 27, "8": 28, "0": 29,
		"o": 31, "u": 32, "i": 34, "p": 35, "l": 37, "j": 38, "k": 40, "n": 45, "m": 46,
		"]": 30, "[": 33, "'": 39, ";": 41, "\\": 42, ",": 43, "/": 44, ".": 47, "`": 50,
		"return": 36, "enter": 36, "tab": 48, "space": 49, "delete": 51, "backspace": 51, "forwarddelete": 117, "escape": 53, "esc": 53, "home": 115, "pageup": 116, "end": 119, "pagedown": 121, "left": 123, "right": 124, "down": 125, "up": 126,
		"f1": 122, "f2": 120, "f3": 99, "f4": 118, "f5": 96, "f6": 97, "f7": 98, "f8": 100, "f9": 101, "f10": 109, "f11": 103, "f12": 111}
	code, ok := codes[parts[len(parts)-1]]
	if !ok {
		return 0, 0, failure(ErrInvalidArgument, "未知按键："+key)
	}
	return code, flags, nil
}

// MockProvider 在没有图形桌面的 CI 中验证完整调用链，不会操作真实应用。
type MockProvider struct {
	mu      sync.Mutex
	text    string
	actions []ResolvedAction
	image   string
}

func NewMockProvider() *MockProvider {
	im := image.NewRGBA(image.Rect(0, 0, 320, 180))
	for y := 0; y < 180; y++ {
		for x := 0; x < 320; x++ {
			im.Set(x, y, color.RGBA{245, 247, 250, 255})
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, im)
	return &MockProvider{image: base64.StdEncoding.EncodeToString(b.Bytes())}
}
func (p *MockProvider) ID() string { return "mock" }
func (p *MockProvider) Capabilities() Capabilities {
	return Capabilities{
		Platform:          "sandbox",
		ObservationModes:  []string{"vision-native"},
		Screenshot:        true,
		AccessibilityTree: true,
		ElementActions:    true,
		BackgroundControl: "full",
		PointClick:        "unsupported",
		Drag:              "unsupported",
		TextInput:         "semantic",
		KeyboardInput:     "pidScoped",
		Isolated:          true,
	}
}
func (p *MockProvider) GetStatus(ctx context.Context) (ProviderStatus, error) {
	return ProviderStatus{Available: true}, ctx.Err()
}
func (p *MockProvider) RequestPermissions(ctx context.Context) (ProviderStatus, error) {
	return p.GetStatus(ctx)
}
func (p *MockProvider) ListApps(ctx context.Context) ([]App, error) {
	return []App{{AppID: "app.notes", Name: "Mock Notes", Windows: []Window{{WindowID: "win-1", Title: "Notes"}}}}, ctx.Err()
}
func (p *MockProvider) CreateLease(ctx context.Context, t Target) (ProviderLease, error) {
	if err := ctx.Err(); err != nil {
		return ProviderLease{}, err
	}
	if t.AppID == "" {
		t.AppID = "app.notes"
	}
	if t.WindowID == "" {
		t.WindowID = "win-1"
	}
	return ProviderLease{
		AppID:          t.AppID,
		WindowID:       t.WindowID,
		AllowedActions: []Action{ActionClickElement, ActionTypeText, ActionPressKey, ActionScroll, ActionSecondary, ActionStop},
		State:          map[string]any{"mock": true},
	}, nil
}
func (p *MockProvider) GetAppState(ctx context.Context, l Lease) (Snapshot, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return Snapshot{
		AppID:            l.AppID,
		WindowID:         l.WindowID,
		Screenshot:       Image{Type: "image", MIMEType: "image/png", Data: p.image},
		Display:          Display{Bounds: Bounds{Width: 320, Height: 180}, ScaleFactor: 1},
		FocusedElementID: "mock-input",
		Elements: []Element{
			{
				ElementID: "mock-button",
				Role:      "button",
				Label:     "Continue",
				Enabled:   true,
				Bounds:    &Bounds{X: 20, Y: 20, Width: 100, Height: 30},
			},
			{
				ElementID: "mock-input",
				Role:      "textbox",
				Label:     "Name",
				Value:     p.text,
				Enabled:   true,
				Bounds:    &Bounds{X: 20, Y: 70, Width: 200, Height: 35},
			},
		},
	}, ctx.Err()
}
func (p *MockProvider) PerformAction(ctx context.Context, l Lease, a ResolvedAction) (ActionResult, error) {
	if err := ctx.Err(); err != nil {
		return ActionResult{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.actions = append(p.actions, clone(a))
	if a.Type == ActionTypeText {
		p.text = a.Text
	}
	return ActionResult{OK: true, Mode: "background", Output: "Mock 动作已执行"}, nil
}
func (p *MockProvider) Actions() []ResolvedAction {
	p.mu.Lock()
	defer p.mu.Unlock()
	return clone(p.actions)
}
func (p *MockProvider) ReleaseLease(context.Context, Lease) error { return nil }
func (p *MockProvider) Stop(context.Context, Lease) error         { return nil }
func (p *MockProvider) Dispose(context.Context) error             { return nil }
