package computeruse

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"sync"
)

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
