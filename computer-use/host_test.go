package computeruse

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testConfig() *Config {
	s := DefaultSettings()
	s.Enabled = true
	return &Config{Settings: &s, Providers: []Provider{NewMockProvider()}, DefaultProviderID: "mock", Session: Session{ID: "session-1", AgentID: "main"}, Model: &ModelInfo{Input: []string{"image"}}}
}
func testHost(t *testing.T, c *Config) *Host {
	t.Helper()
	h, err := NewHost(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := h.Dispose(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return h
}
func mustLease(t *testing.T, h *Host, ctx context.Context) Lease {
	t.Helper()
	l, err := h.CreateLease(ctx, Target{AppID: "app.notes"})
	if err != nil {
		t.Fatal(err)
	}
	return l
}
func mustSnapshot(t *testing.T, h *Host, ctx context.Context, l Lease) Snapshot {
	t.Helper()
	s, err := h.GetAppState(ctx, l.LeaseID)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func TestHostSnapshotOwnershipAndFreshness(t *testing.T) {
	ctx := context.Background()
	h := testHost(t, testConfig())
	l := mustLease(t, h, ctx)
	old := mustSnapshot(t, h, ctx, l)
	fresh := mustSnapshot(t, h, ctx, l)
	_, err := h.PerformAction(ctx, l.LeaseID, ResolvedAction{Type: ActionClickElement, SnapshotID: old.SnapshotID, ElementID: "mock-button"})
	if !ErrorHasCode(err, ErrStaleSnapshot) {
		t.Fatalf("old snapshot: %v", err)
	}
	other := WithSession(ctx, Session{ID: "another-session", AgentID: "main"})
	if _, err = h.GetAppState(other, l.LeaseID); !ErrorHasCode(err, ErrLeaseNotFound) {
		t.Fatalf("cross-session read: %v", err)
	}
	_, err = h.PerformAction(ctx, l.LeaseID, ResolvedAction{Type: ActionTypeText, SnapshotID: fresh.SnapshotID, ElementID: "mock-input", Text: "中文输入"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = h.PerformAction(ctx, l.LeaseID, ResolvedAction{Type: ActionClickElement, ElementID: "mock-button"}); !ErrorHasCode(err, ErrStaleSnapshot) {
		t.Fatalf("action must invalidate snapshot: %v", err)
	}
	next := mustSnapshot(t, h, ctx, l)
	if next.Elements[1].Value != "中文输入" {
		t.Fatalf("text not reflected in next observation: %+v", next.Elements)
	}
	if err = h.Stop(ctx, l.LeaseID); err != nil {
		t.Fatal(err)
	}
	if _, err = h.GetAppState(ctx, l.LeaseID); !ErrorHasCode(err, ErrLeaseReleased) {
		t.Fatalf("released lease: %v", err)
	}
	if len(h.leases.snapshots) != 0 {
		t.Fatal("released image retained")
	}
}
func TestHostPolicyGates(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Config)
		code   ErrorCode
	}{{"disabled", func(c *Config) { c.Settings.Enabled = false }, ErrDisabled}, {"text model", func(c *Config) { c.Model = &ModelInfo{Input: []string{"text"}} }, ErrRequiresVisionModel}, {"missing model", func(c *Config) { c.Model = nil }, ErrRequiresVisionModel}, {"read only", func(c *Config) { c.Session.AccessMode = "read-only" }, ErrDisabled}, {"other agent", func(c *Config) { c.PrimaryAgentID = "other" }, ErrDisabled}} {
		t.Run(tc.name, func(t *testing.T) {
			c := testConfig()
			tc.change(c)
			h := testHost(t, c)
			if _, err := h.CreateLease(context.Background(), Target{AppID: "app.notes"}); !ErrorHasCode(err, tc.code) {
				t.Fatalf("got %v", err)
			}
		})
	}
}
func TestHostReuseTakeoverAndSessionIDMigration(t *testing.T) {
	ctx := context.Background()
	h := testHost(t, testConfig())
	l := mustLease(t, h, ctx)
	moved := WithSession(ctx, Session{ID: "session-1", Path: "/new/path", AgentID: "main"})
	again := mustLease(t, h, moved)
	if again.LeaseID != l.LeaseID {
		t.Fatal("same identity should reuse")
	}
	other := WithSession(ctx, Session{ID: "session-2", AgentID: "main"})
	next := mustLease(t, h, other)
	if next.LeaseID == l.LeaseID {
		t.Fatal("new owner should take over")
	}
	if _, err := h.GetAppState(ctx, l.LeaseID); !ErrorHasCode(err, ErrLeaseReleased) {
		t.Fatalf("old lease still active: %v", err)
	}
}

type observedProvider struct {
	*MockProvider
	isolated  bool
	stopFn    func()
	actFn     func(context.Context) error
	inFlight  atomic.Int32
	maxFlight atomic.Int32
}

type failingObservationProvider struct {
	*MockProvider
	fail bool
}

func (p *failingObservationProvider) GetAppState(ctx context.Context, l Lease) (Snapshot, error) {
	if p.fail {
		return Snapshot{}, failure(ErrProviderCrashed, "截图失败，但原生元素缓存已经更新")
	}
	return p.MockProvider.GetAppState(ctx, l)
}
func TestFailedObservationInvalidatesPreviousVersion(t *testing.T) {
	p := &failingObservationProvider{MockProvider: NewMockProvider()}
	c := testConfig()
	c.Providers = []Provider{p}
	h := testHost(t, c)
	ctx := context.Background()
	l := mustLease(t, h, ctx)
	s := mustSnapshot(t, h, ctx, l)
	p.fail = true
	if _, err := h.GetAppState(ctx, l.LeaseID); err == nil {
		t.Fatal("expected observation error")
	}
	if _, err := h.PerformAction(ctx, l.LeaseID, ResolvedAction{Type: ActionClickElement, SnapshotID: s.SnapshotID, ElementID: "mock-button"}); !ErrorHasCode(err, ErrStaleSnapshot) {
		t.Fatalf("failed observation left old element version usable: %v", err)
	}
}

type invalidStateProvider struct {
	*MockProvider
	invalidLease bool
}

func (p *invalidStateProvider) CreateLease(ctx context.Context, target Target) (ProviderLease, error) {
	l, err := p.MockProvider.CreateLease(ctx, target)
	if p.invalidLease {
		l.State = map[string]any{"invalid": func() {}}
	}
	return l, err
}
func (p *invalidStateProvider) GetAppState(ctx context.Context, l Lease) (Snapshot, error) {
	s, err := p.MockProvider.GetAppState(ctx, l)
	s.Elements[0].Value = func() {}
	return s, err
}
func TestInvalidProviderJSONReturnsErrorInsteadOfPanic(t *testing.T) {
	for _, invalidLease := range []bool{true, false} {
		c := testConfig()
		c.Providers = []Provider{&invalidStateProvider{MockProvider: NewMockProvider(), invalidLease: invalidLease}}
		h := testHost(t, c)
		l, err := h.CreateLease(context.Background(), Target{AppID: "app.notes"})
		if !invalidLease && err == nil {
			_, err = h.GetAppState(context.Background(), l.LeaseID)
		}
		if !ErrorHasCode(err, ErrProviderCrashed) {
			t.Fatalf("non-JSON provider state accepted: %v", err)
		}
	}
}

func (p *observedProvider) Capabilities() Capabilities {
	c := p.MockProvider.Capabilities()
	c.Isolated = p.isolated
	return c
}
func (p *observedProvider) Stop(context.Context, Lease) error {
	if p.stopFn != nil {
		p.stopFn()
	}
	return nil
}
func (p *observedProvider) PerformAction(ctx context.Context, l Lease, a ResolvedAction) (ActionResult, error) {
	n := p.inFlight.Add(1)
	defer p.inFlight.Add(-1)
	for {
		old := p.maxFlight.Load()
		if n <= old || p.maxFlight.CompareAndSwap(old, n) {
			break
		}
	}
	if p.actFn != nil {
		if err := p.actFn(ctx); err != nil {
			return ActionResult{}, err
		}
	}
	return p.MockProvider.PerformAction(ctx, l, a)
}
func TestApprovalModesAndRevocation(t *testing.T) {
	ctx := context.Background()
	p := &observedProvider{MockProvider: NewMockProvider()}
	c := testConfig()
	c.Providers = []Provider{p}
	h := testHost(t, c)
	if _, err := h.CreateLease(ctx, Target{AppID: "app.notes"}); !ErrorHasCode(err, ErrAppApprovalRequired) {
		t.Fatal(err)
	}
	if err := h.ApproveApp(ctx, AppApproval{ProviderID: "mock", AppID: "app.notes"}); err != nil {
		t.Fatal(err)
	}
	l := mustLease(t, h, ctx)
	if err := h.RevokeApp(ctx, "mock", "app.notes"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.GetAppState(ctx, l.LeaseID); !ErrorHasCode(err, ErrLeaseReleased) {
		t.Fatalf("revocation must stop active lease: %v", err)
	}
	for _, mode := range []PermissionMode{PermissionAsk, PermissionAuto, PermissionOperate} {
		t.Run(string(mode), func(t *testing.T) {
			c := testConfig()
			c.Providers = []Provider{&observedProvider{MockProvider: NewMockProvider()}}
			c.PermissionMode = mode
			var review, confirm int
			handler := func(_ context.Context, r ApprovalRequest) (ApprovalDecision, error) {
				if r.Approval.AppID != "app.notes" {
					t.Fatal("wrong approval target")
				}
				return ApprovalDecision{Status: "approved", Scope: "session"}, nil
			}
			c.ReviewApp = func(ctx context.Context, r ApprovalRequest) (ApprovalDecision, error) {
				review++
				return handler(ctx, r)
			}
			c.ConfirmApp = func(ctx context.Context, r ApprovalRequest) (ApprovalDecision, error) {
				confirm++
				return handler(ctx, r)
			}
			h := testHost(t, c)
			mustLease(t, h, ctx)
			if (mode == PermissionAuto && review != 1) || (mode == PermissionAsk && confirm != 1) || (mode == PermissionOperate && (review != 0 || confirm != 0)) {
				t.Fatalf("wrong callback routing: review=%d confirm=%d", review, confirm)
			}
		})
	}
}
func TestDeniedStartDoesNotTakeOver(t *testing.T) {
	c := testConfig()
	p := &observedProvider{MockProvider: NewMockProvider()}
	c.Providers = []Provider{p}
	c.Settings.AppApprovals = []AppApproval{{ProviderID: "mock", AppID: "app.notes"}}
	h := testHost(t, c)
	ctx := context.Background()
	l := mustLease(t, h, ctx)
	_, err := h.CreateLease(WithSession(ctx, Session{ID: "other", AgentID: "main"}), Target{AppID: "unapproved.app"})
	if !ErrorHasCode(err, ErrAppApprovalRequired) {
		t.Fatal(err)
	}
	mustSnapshot(t, h, ctx, l)
}
func TestLeaseAndSnapshotTTL(t *testing.T) {
	ctx := context.Background()
	h := testHost(t, testConfig())
	l := mustLease(t, h, ctx)
	s := mustSnapshot(t, h, ctx, l)
	h.leases.mu.Lock()
	shot := h.leases.snapshots[l.LeaseID]
	shot.CapturedAt = time.Now().Add(-time.Hour)
	h.leases.snapshots[l.LeaseID] = shot
	h.leases.mu.Unlock()
	h.config.SnapshotTTL = time.Minute
	if _, err := h.PerformAction(ctx, l.LeaseID, ResolvedAction{Type: ActionClickElement, SnapshotID: s.SnapshotID, ElementID: "mock-button"}); !ErrorHasCode(err, ErrStaleSnapshot) {
		t.Fatal(err)
	}
	h.leases.mu.Lock()
	lease := h.leases.leases[l.LeaseID]
	expired := time.Now().Add(-time.Hour)
	lease.ExpiresAt = &expired
	h.leases.leases[l.LeaseID] = lease
	h.leases.mu.Unlock()
	if _, err := h.GetAppState(ctx, l.LeaseID); !ErrorHasCode(err, ErrLeaseReleased) {
		t.Fatal(err)
	}
}
func TestStopCancelsActiveAction(t *testing.T) {
	entered := make(chan struct{})
	p := &observedProvider{MockProvider: NewMockProvider(), isolated: true, actFn: func(ctx context.Context) error { close(entered); <-ctx.Done(); return ctx.Err() }}
	c := testConfig()
	c.Providers = []Provider{p}
	h := testHost(t, c)
	ctx := context.Background()
	l := mustLease(t, h, ctx)
	s := mustSnapshot(t, h, ctx, l)
	done := make(chan error, 1)
	go func() {
		_, err := h.PerformAction(ctx, l.LeaseID, ResolvedAction{Type: ActionClickElement, SnapshotID: s.SnapshotID, ElementID: "mock-button"})
		done <- err
	}()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("action did not start")
	}
	stopCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if err := h.Stop(stopCtx, l.LeaseID); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("action was not cancelled: %v", err)
	}
}
func TestActionsSerializedAndQueueCancellable(t *testing.T) {
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	p := &observedProvider{MockProvider: NewMockProvider(), isolated: true, actFn: func(ctx context.Context) error {
		entered <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
	c := testConfig()
	c.PreserveSnapshotAfterAction = true
	c.Providers = []Provider{p}
	h := testHost(t, c)
	ctx := context.Background()
	l := mustLease(t, h, ctx)
	s := mustSnapshot(t, h, ctx, l)
	a := ResolvedAction{Type: ActionClickElement, SnapshotID: s.SnapshotID, ElementID: "mock-button"}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if _, err := h.PerformAction(ctx, l.LeaseID, a); err != nil {
			t.Error(err)
		}
	}()
	<-entered
	short, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	if _, err := h.GetAppState(short, l.LeaseID); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued operation ignored cancellation: %v", err)
	}
	close(release)
	wg.Wait()
	if p.maxFlight.Load() != 1 {
		t.Fatal("operations overlapped")
	}
}
func TestSnapshotMetadataCannotBeForged(t *testing.T) {
	c := testConfig()
	p := c.Providers[0].(*MockProvider)
	h := testHost(t, c)
	ctx := context.Background()
	l := mustLease(t, h, ctx)
	s := mustSnapshot(t, h, ctx, l)
	_, err := h.PerformAction(ctx, l.LeaseID, ResolvedAction{Type: ActionClickElement, SnapshotID: s.SnapshotID, ElementID: "mock-button", SnapshotElement: &Element{ElementID: "injected", Label: "forged"}})
	if err != nil {
		t.Fatal(err)
	}
	actions := p.Actions()
	if actions[0].SnapshotElement.Label != "Continue" {
		t.Fatal("untrusted metadata forwarded")
	}
}
