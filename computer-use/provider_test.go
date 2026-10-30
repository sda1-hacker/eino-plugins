package computeruse

import (
	"context"
	"testing"
	"time"
)

// timeoutDriver 不接触桌面，只验证 Provider 会传递并执行操作超时。
type timeoutDriver struct{ closes int }

func (*timeoutDriver) Status(context.Context, bool) (ProviderStatus, error) {
	return ProviderStatus{}, nil
}
func (*timeoutDriver) ListApps(context.Context) ([]App, error) { return nil, nil }
func (*timeoutDriver) CreateLease(context.Context, Target) (ProviderLease, error) {
	return ProviderLease{}, nil
}
func (*timeoutDriver) Act(context.Context, Lease, ResolvedAction) (ActionResult, error) {
	return ActionResult{}, nil
}
func (*timeoutDriver) Observe(ctx context.Context, _ Lease) (Snapshot, error) {
	<-ctx.Done()
	return Snapshot{}, ctx.Err()
}
func (d *timeoutDriver) Close() error { d.closes++; return nil }

func TestMacOSOperationTimeoutAndCleanup(t *testing.T) {
	driver := &timeoutDriver{}
	p := NewMacOSProvider(&MacOSConfig{Platform: "darwin", Timeout: 10 * time.Millisecond})
	p.driver = driver
	if _, err := p.GetAppState(context.Background(), Lease{}); err != context.DeadlineExceeded {
		t.Fatalf("系统操作应受配置超时限制：%v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Dispose(ctx); err != nil || driver.closes != 1 {
		t.Fatal("调用上下文取消后仍应释放已拥有的对象", err, driver.closes)
	}
}

func TestMacOSPlatformAndCancellation(t *testing.T) {
	p := NewMacOSProvider(&MacOSConfig{Platform: "linux"})
	status, err := p.GetStatus(context.Background())
	if err != nil || status.Available || status.Reason != "unsupported-platform" {
		t.Fatal(status, err)
	}
	if _, err := p.ListApps(context.Background()); !ErrorHasCode(err, ErrProviderUnavailable) {
		t.Fatal("不支持的平台不能加载原生后端", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.GetAppState(ctx, Lease{}); err != context.Canceled || p.driver != nil {
		t.Fatal("已取消的操作不能初始化系统框架", err)
	}
	if err := p.Dispose(ctx); err != nil || p.driver != nil {
		t.Fatal("无操作的清理不能初始化系统框架", err)
	}
}

func TestMacOSLeaseDoesNotSubstituteRequestedWindow(t *testing.T) {
	app := App{AppID: "app.test", PID: 42}
	windows := []Window{{WindowID: "7", Title: "主窗口"}, {WindowID: "8", Title: "第二窗口"}}
	if _, err := macLease(app, windows, Target{WindowID: "9"}); !ErrorHasCode(err, ErrTargetNotFound) {
		t.Fatal("指定窗口不存在时不能替换为其他窗口", err)
	}
	lease, err := macLease(app, windows, Target{WindowID: "8"})
	if err != nil || lease.WindowID != "8" || integer(lease.State["pid"]) != 42 {
		t.Fatal(lease, err)
	}
}

func TestMacOSDefaultNativeProvider(t *testing.T) {
	p := NewMacOSProvider(nil)
	if p.ID() != "macos:ax" || p.driver != nil {
		t.Fatal("构造应只保存配置，系统框架按需加载")
	}
	if caps := p.Capabilities(); caps.NativeCursor || caps.BackgroundControl != "partial" {
		t.Fatal("不能声明未实现的光标或完全后台控制能力", caps)
	}
	h, err := NewHost(&Config{Platform: "darwin", Providers: []Provider{p}})
	if err != nil {
		t.Fatal(err)
	}
	if h.providerID("") != "macos:ax" {
		t.Fatal("默认 Provider 应选择原生后端")
	}
}

func TestMacKeyCode(t *testing.T) {
	for _, test := range []struct {
		key   string
		code  uint16
		flags uint64
	}{
		{"Command+Shift+A", 0, 1<<20 | 1<<17},
		{"ctrl+option+left", 123, 1<<18 | 1<<19},
		{"return", 36, 0}, {"forwarddelete", 117, 0}, {";", 41, 0},
	} {
		code, flags, err := macKeyCode(test.key)
		if err != nil || code != test.code || flags != test.flags {
			t.Errorf("%q: %d %d %v", test.key, code, flags, err)
		}
	}
	for _, key := range []string{"", "cmd+", "unknown+a", "not-a-key"} {
		if _, _, err := macKeyCode(key); err == nil {
			t.Errorf("应拒绝无效按键 %q", key)
		}
	}
}

func TestMatchMacWindow(t *testing.T) {
	bounds := &Bounds{X: 100, Y: 100, Width: 800, Height: 600}
	first := Window{Title: "第一窗口", Bounds: bounds}
	second := Window{Title: "第二窗口", Bounds: bounds}
	if got := matchMacWindow([]Window{first, second}, second); got != 1 {
		t.Fatalf("重叠窗口应按标题消歧，得到 %d", got)
	}
	if got := matchMacWindow([]Window{first, first}, first); got != -1 {
		t.Fatal("同名重叠窗口必须拒绝猜测")
	}
	if got := matchMacWindow([]Window{first}, Window{Bounds: &Bounds{X: 102, Y: 101, Width: 800, Height: 600}}); got != 0 {
		t.Fatal("唯一窗口应容忍系统边框的微小差异")
	}
	if got := matchMacWindow([]Window{first}, Window{Bounds: &Bounds{X: 300, Y: 100, Width: 800, Height: 600}}); got != -1 {
		t.Fatal("不应匹配其他位置的窗口")
	}
}
