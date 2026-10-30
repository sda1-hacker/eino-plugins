package computeruse

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type fakeRunner struct {
	run   func(context.Context, Command) (CommandResult, error)
	start func(Command) (ManagedProcess, error)
}

func (f *fakeRunner) Run(ctx context.Context, c Command) (CommandResult, error) { return f.run(ctx, c) }
func (f *fakeRunner) Start(c Command) (ManagedProcess, error) {
	if f.start == nil {
		return nil, errors.New("unexpected daemon start")
	}
	return f.start(c)
}

type fakeProcess struct{ done chan struct{} }

func (p *fakeProcess) Kill() error {
	select {
	case <-p.done:
	default:
		close(p.done)
	}
	return nil
}
func (p *fakeProcess) Done() <-chan struct{} { return p.done }
func jsonCommand(v any) CommandResult {
	b, _ := json.Marshal(v)
	return CommandResult{Stdout: string(b)}
}
func TestMacOSDaemonAndElementProtocol(t *testing.T) {
	ctx := context.Background()
	proc := &fakeProcess{done: make(chan struct{})}
	running := false
	starts := 0
	stops := 0
	var actions []map[string]any
	image := NewMockProvider().image
	runner := &fakeRunner{}
	runner.start = func(c Command) (ManagedProcess, error) {
		if c.Args[0] != "serve" {
			t.Fatal(c.Args)
		}
		if c.Env["HANA_AGENT_CURSOR_CONFIG_JSON"] == "" {
			t.Fatal("missing native cursor config")
		}
		running = true
		starts++
		return proc, nil
	}
	runner.run = func(ctx context.Context, c Command) (CommandResult, error) {
		switch c.Args[0] {
		case "status":
			if !running {
				return CommandResult{ExitCode: 1}, nil
			}
			return CommandResult{Stdout: "running"}, nil
		case "stop":
			running = false
			stops++
			_ = proc.Kill()
			return CommandResult{}, nil
		case "set_agent_cursor_enabled":
			return jsonCommand(map[string]any{}), nil
		case "list_apps":
			return jsonCommand(map[string]any{"structuredContent": map[string]any{"apps": []any{map[string]any{"bundle_id": "app.notes", "name": "Notes", "pid": 42}}}}), nil
		case "list_windows":
			return jsonCommand(map[string]any{"structuredContent": map[string]any{"windows": []any{map[string]any{"window_id": 7, "title": "Notes", "is_on_screen": true}}}}), nil
		case "get_window_state":
			return jsonCommand(map[string]any{"content": []any{map[string]any{"type": "image", "mimeType": "image/jpeg", "data": image}}, "structuredContent": map[string]any{"tree_markdown": "- [0] AXRow \"笔记\" actions=[AXShowDefaultUI]", "screenshot_width": 320, "screenshot_height": 180}}), nil
		case "click":
			var a map[string]any
			if err := json.Unmarshal([]byte(c.Args[1]), &a); err != nil {
				t.Fatal(err)
			}
			actions = append(actions, a)
			return jsonCommand(map[string]any{"content": []any{map[string]any{"type": "text", "text": "warning: action may be a no-op"}}}), nil
		default:
			t.Fatalf("unexpected command: %+v", c)
			return CommandResult{}, nil
		}
	}
	p := NewMacOSProvider(&MacOSConfig{Platform: "darwin", Command: "/test/hana-computer-use-helper", SocketPath: filepath.Join(t.TempDir(), "helper.sock"), Runner: runner})
	lease, err := p.CreateLease(ctx, Target{AppID: "app.notes"})
	if err != nil {
		t.Fatal(err)
	}
	l := Lease{ProviderLease: lease}
	s, err := p.GetAppState(ctx, l)
	if err != nil {
		t.Fatal(err)
	}
	if s.Screenshot.MIMEType != "image/jpeg" || len(s.Elements) != 1 || s.Elements[0].ElementID != "0" {
		t.Fatalf("snapshot normalization: %+v", s)
	}
	result, err := p.PerformAction(ctx, l, ResolvedAction{Type: ActionClickElement, ElementID: "0", SnapshotElement: &s.Elements[0]})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, "no-op") {
		t.Fatal("driver warning lost")
	}
	if len(actions) != 1 || actions[0]["action"] != "show_default_ui" || actions[0]["element_index"] != float64(0) {
		t.Fatalf("semantic mapping: %+v", actions)
	}
	if _, err = p.PerformAction(ctx, l, ResolvedAction{Type: ActionClickPoint}); !ErrorHasCode(err, ErrCapabilityUnsupported) {
		t.Fatal(err)
	}
	if err = p.Dispose(ctx); err != nil {
		t.Fatal(err)
	}
	if starts != 1 || stops != 1 {
		t.Fatalf("daemon lifecycle: %d/%d", starts, stops)
	}
}
func TestMacOSDoesNotStopExternalDaemon(t *testing.T) {
	managed := false
	runner := &fakeRunner{run: func(_ context.Context, c Command) (CommandResult, error) {
		if c.Args[0] == "stop" {
			managed = true
		}
		return jsonCommand(map[string]any{"structuredContent": []any{}}), nil
	}}
	auto := false
	p := NewMacOSProvider(&MacOSConfig{Platform: "darwin", Command: "cua-driver", AutoStartDaemon: &auto, Runner: runner})
	if _, err := p.ListApps(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := p.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	if managed {
		t.Fatal("external daemon was stopped")
	}
}
func TestMacOSMissingImageAndPermissionError(t *testing.T) {
	l := Lease{ProviderLease: ProviderLease{State: map[string]any{"pid": 1, "windowId": 2}}}
	auto := false
	runner := &fakeRunner{run: func(context.Context, Command) (CommandResult, error) {
		return jsonCommand(map[string]any{"structuredContent": map[string]any{"tree_markdown": ""}}), nil
	}}
	p := NewMacOSProvider(&MacOSConfig{Platform: "darwin", Command: "cua-driver", AutoStartDaemon: &auto, Runner: runner})
	if _, err := p.GetAppState(context.Background(), l); !ErrorHasCode(err, ErrProviderCrashed) {
		t.Fatal(err)
	}
	runner.run = func(context.Context, Command) (CommandResult, error) {
		return CommandResult{ExitCode: 1, Stderr: "Screen recording permission denied"}, nil
	}
	if _, err := p.ListApps(context.Background()); !ErrorHasCode(err, ErrOSPermissionDenied) {
		t.Fatal(err)
	}
}
func TestDriverPathPrecedence(t *testing.T) {
	root := t.TempDir()
	explicit := filepath.Join(root, "missing-helper")
	if got := ResolveCUADriverCommand(MacOSConfig{Env: map[string]string{"HANA_COMPUTER_USE_HELPER_PATH": explicit}}); got != explicit {
		t.Fatal(got)
	}
	resources := filepath.Join(root, "Hana.app", "Contents", "Resources")
	got := ResolveCUADriverCommand(MacOSConfig{Env: map[string]string{"HANA_DESKTOP_IS_PACKAGED": "1", "HANA_DESKTOP_RESOURCES_PATH": resources, "HANA_COMPUTER_USE_HELPER_PATH": "", "HANA_COMPUTER_USE_RUNTIME_ROOT": "", "HANA_ROOT": ""}})
	want := filepath.Join(resources, "computer-use", "macos", "hana-computer-use-helper")
	if got != want {
		t.Fatalf("missing packaged helper must remain authoritative: %s", got)
	}
	if _, err := os.Stat(want); !os.IsNotExist(err) {
		t.Fatal(err)
	}
}
func TestMacOSAsyncLaunchFailure(t *testing.T) {
	runner := &fakeRunner{run: func(context.Context, Command) (CommandResult, error) { return CommandResult{ExitCode: 1}, nil }, start: func(Command) (ManagedProcess, error) { return nil, os.ErrNotExist }}
	p := NewMacOSProvider(&MacOSConfig{Platform: "darwin", Command: "/missing/hana-computer-use-helper", SocketPath: filepath.Join(t.TempDir(), "sock"), Runner: runner})
	if _, err := p.ListApps(context.Background()); !ErrorHasCode(err, ErrProviderUnavailable) {
		t.Fatal(err)
	}
}

func TestMacOSTimeoutResetsOwnedDaemon(t *testing.T) {
	proc := &fakeProcess{done: make(chan struct{})}
	stopped := false
	runner := &fakeRunner{run: func(ctx context.Context, c Command) (CommandResult, error) {
		if c.Args[0] == "stop" {
			stopped = true
			_ = proc.Kill()
			return CommandResult{}, nil
		}
		return CommandResult{}, context.DeadlineExceeded
	}}
	auto := false
	p := NewMacOSProvider(&MacOSConfig{Platform: "darwin", Command: "cua-driver", AutoStartDaemon: &auto, Runner: runner})
	p.process = proc
	if _, err := p.ListApps(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if !stopped || p.process != nil {
		t.Fatal("timed out request left managed daemon running")
	}
}

func TestCuaMarkdownPreservesDescriptionAndTextValue(t *testing.T) {
	elements := parseTree(`- [0] AXTextField (Eino 验证输入框) id=eino-validation-input actions=[AXConfirm]
- [1] AXTextField = "Eino 真实桌面验证：中文输入成功" (Eino 验证输入框) id=eino-validation-input actions=[AXConfirm]
- [2] AXButton "Eino 验证按钮，已点击 1 次"
- [3] AXTextField "金额 (元)" = "100 (含税)" (金额输入框) id=amount`)
	if len(elements) != 4 {
		t.Fatalf("elements: %+v", elements)
	}
	for _, index := range []int{0, 1} {
		if elements[index].Label != "Eino 验证输入框" || elements[index].AutomationID != "eino-validation-input" {
			t.Fatalf("AXDescription or identifier lost: %+v", elements[index])
		}
	}
	if elements[1].Value != "Eino 真实桌面验证：中文输入成功" {
		t.Fatalf("AXValue lost: %+v", elements[1])
	}
	if elements[2].Label != "Eino 验证按钮，已点击 1 次" {
		t.Fatal(elements[2])
	}
	if elements[3].Label != "金额 (元)" || elements[3].Value != "100 (含税)" {
		t.Fatal(elements[3])
	}
}
