package computeruse

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func argument(args []string, name string) string {
	for i, v := range args {
		if v == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}
func TestWindowsFileProtocolAndSnapshotBinding(t *testing.T) {
	image := NewMockProvider().image
	var requests []string
	var action map[string]any
	runner := &fakeRunner{run: func(_ context.Context, c Command) (CommandResult, error) {
		requestPath := argument(c.Args, "-RequestPath")
		resultPath := argument(c.Args, "-ResultPath")
		requests = append(requests, requestPath)
		if !strings.HasSuffix(argument(c.Args, "-File"), ".ps1") {
			t.Fatal("helper must run as a file")
		}
		b, err := os.ReadFile(requestPath)
		if err != nil {
			t.Fatal(err)
		}
		var req map[string]any
		if err = json.Unmarshal(b, &req); err != nil {
			t.Fatal(err)
		}
		data := map[string]any{}
		switch req["command"] {
		case "list_apps":
			data["apps"] = []any{map[string]any{"appId": "pid:123", "name": "记事本", "processId": 123, "windows": []any{map[string]any{"windowId": "99", "title": "记事本"}}}}
		case "get_app_state":
			data = map[string]any{"windowId": "99", "screenshot": map[string]any{"data": image, "width": 320, "height": 180, "scaleFactor": 0.5}, "elements": []any{map[string]any{"elementId": "uia:0", "role": "ControlType.Edit", "label": "编辑", "enabled": true, "automationId": "input"}}}
		case "perform_action":
			action = object(req["action"])
			data = map[string]any{"ok": true, "mode": "background"}
		default:
			t.Fatal(req["command"])
		}
		result, _ := json.Marshal(map[string]any{"ok": true, "data": data})
		if err = os.WriteFile(resultPath, result, 0600); err != nil {
			t.Fatal(err)
		}
		return CommandResult{Stdout: "PowerShell noise"}, nil
	}}
	p := NewWindowsProvider(&WindowsConfig{Platform: "windows", Runner: runner, HelperDir: t.TempDir()})
	ctx := context.Background()
	apps, err := p.ListApps(ctx)
	if err != nil || len(apps) != 1 {
		t.Fatalf("apps: %+v %v", apps, err)
	}
	lease, err := p.CreateLease(ctx, Target{AppID: "pid:123", WindowID: "99"})
	if err != nil {
		t.Fatal(err)
	}
	l := Lease{ProviderLease: lease}
	s, err := p.GetAppState(ctx, l)
	if err != nil {
		t.Fatal(err)
	}
	if s.Display.ScaleFactor != 0.5 || s.Elements[0].AutomationID != "input" {
		t.Fatalf("state: %+v", s)
	}
	_, err = p.PerformAction(ctx, l, ResolvedAction{Type: ActionTypeText, ElementID: "uia:0", SnapshotElement: &s.Elements[0], Text: "引号 ' \" 与中文"})
	if err != nil {
		t.Fatal(err)
	}
	if object(action["snapshotElement"])["automationId"] != "input" || action["text"] != "引号 ' \" 与中文" {
		t.Fatalf("lost binding or text: %+v", action)
	}
	for _, path := range requests {
		if _, err = os.Stat(path); !os.IsNotExist(err) {
			t.Fatal("request was not cleaned")
		}
	}
	count := len(requests)
	if _, err = p.PerformAction(ctx, l, ResolvedAction{Type: ActionTypeText, Text: "foreground"}); !ErrorHasCode(err, ErrRequiresForeground) {
		t.Fatal(err)
	}
	if _, err = p.PerformAction(ctx, l, ResolvedAction{Type: ActionClickElement, ElementID: "uia:0"}); !ErrorHasCode(err, ErrStaleSnapshot) {
		t.Fatal(err)
	}
	if len(requests) != count {
		t.Fatal("blocked action invoked helper")
	}
}
func TestWindowsResultLimitsAndTypedErrors(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		limit      int64
		code       ErrorCode
	}{{"invalid JSON", "not json", 4096, ErrProviderCrashed}, {"oversized", strings.Repeat("x", 100), 20, ErrProviderCrashed}, {"missing result", "", 4096, ErrProviderCrashed}, {"typed error", `{"ok":false,"errorCode":"STALE_SNAPSHOT","message":"refresh","details":{"reason":"label-mismatch"}}`, 4096, ErrStaleSnapshot}} {
		t.Run(tc.name, func(t *testing.T) {
			runner := &fakeRunner{run: func(_ context.Context, c Command) (CommandResult, error) {
				if tc.body != "" {
					if err := os.WriteFile(argument(c.Args, "-ResultPath"), []byte(tc.body), 0600); err != nil {
						t.Fatal(err)
					}
				}
				return CommandResult{}, nil
			}}
			p := NewWindowsProvider(&WindowsConfig{Platform: "windows", Runner: runner, HelperDir: t.TempDir(), MaxResultBytes: tc.limit})
			if _, err := p.ListApps(context.Background()); !ErrorHasCode(err, tc.code) {
				t.Fatal(err)
			}
		})
	}
}
func TestWindowsScriptAndHelperCleanup(t *testing.T) {
	if !strings.Contains(windowsUIAScript, "$scrollStep -lt $n") || !strings.Contains(windowsUIAScript, "Test-Element-Matches-Snapshot") || !strings.Contains(windowsUIAScript, "CopyFromScreen") {
		t.Fatal("upstream UIA features missing")
	}
	p := NewWindowsProvider(&WindowsConfig{Platform: "windows"})
	path, err := p.prepareHelper()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Dir(path)
	if err = p.Dispose(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("owned helper directory retained")
	}
}
