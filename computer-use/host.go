package computeruse

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Host 将 observe、act、takeover 串行化，避免并发调用替换底层元素缓存。
type Host struct {
	config         Config
	settings       Settings
	providers      *ProviderRegistry
	leases         *LeaseRegistry
	gate           chan struct{}
	closed         bool
	currentMu      sync.Mutex
	currentCancel  context.CancelFunc
	currentSession Session
}

func NewHost(config *Config) (*Host, error) {
	c := Config{}
	if config != nil {
		c = *config
	}
	if c.Platform == "" {
		c.Platform = runtime.GOOS
	}
	if c.Platform == "win32" {
		c.Platform = "windows"
	}
	if c.PermissionMode == "" {
		c.PermissionMode = PermissionAsk
	}
	if c.PermissionMode != PermissionAsk && c.PermissionMode != PermissionAuto && c.PermissionMode != PermissionOperate {
		return nil, fmt.Errorf("无效 PermissionMode: %s", c.PermissionMode)
	}
	if c.Session.ID == "" && c.Session.Path == "" {
		c.Session.ID = newID()
	}
	if c.Session.Model == nil {
		c.Session.Model = cloneModel(c.Model)
	} else {
		c.Session.Model = cloneModel(c.Session.Model)
	}
	if c.MaxSnapshotBytes <= 0 {
		c.MaxSnapshotBytes = 32 << 20
	}
	r := NewProviderRegistry()
	providers := c.Providers
	if len(providers) == 0 {
		providers = []Provider{NewMockProvider(), NewMacOSProvider(c.MacOS), NewWindowsProvider(c.Windows)}
	}
	for _, p := range providers {
		if err := r.Register(p); err != nil {
			return nil, err
		}
	}
	s := DefaultSettings()
	if c.Settings != nil {
		s = normalizeSettings(*c.Settings)
	}
	return &Host{config: c, settings: s, providers: r, leases: NewLeaseRegistry(), gate: make(chan struct{}, 1)}, nil
}
func cloneModel(m *ModelInfo) *ModelInfo {
	if m == nil {
		return nil
	}
	c := clone(*m)
	return &c
}
func (h *Host) session(ctx context.Context) Session {
	s, ok := ctx.Value(sessionKey{}).(Session)
	if !ok {
		return h.config.Session
	}
	if s.ID == "" && s.Path == "" {
		s.ID = h.config.Session.ID
		s.Path = h.config.Session.Path
	}
	if s.AgentID == "" {
		s.AgentID = h.config.Session.AgentID
	}
	if s.Model == nil {
		s.Model = h.config.Session.Model
	}
	return s
}
func (h *Host) enter(ctx context.Context) (context.Context, func(), error) {
	select {
	case h.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		<-h.gate
		return nil, nil, err
	}
	if h.closed {
		<-h.gate
		return nil, nil, failure(ErrDisabled, "Host 已关闭")
	}
	op, cancel := context.WithCancel(ctx)
	h.currentMu.Lock()
	h.currentCancel = cancel
	h.currentSession = h.session(ctx)
	h.currentMu.Unlock()
	return op, func() { h.currentMu.Lock(); h.currentCancel = nil; h.currentMu.Unlock(); cancel(); <-h.gate }, nil
}
func (h *Host) cancelCurrent(s *Session) {
	h.currentMu.Lock()
	defer h.currentMu.Unlock()
	if h.currentCancel != nil && (s == nil || sameOwner(Lease{SessionID: h.currentSession.ID, SessionPath: h.currentSession.Path, AgentID: h.currentSession.AgentID}, *s)) {
		h.currentCancel()
	}
}
func (h *Host) assertRuntime(s Session) error {
	if !h.settings.Enabled {
		return failure(ErrDisabled, "Computer Use 尚未启用")
	}
	if h.config.PrimaryAgentID != "" && s.AgentID != h.config.PrimaryAgentID {
		return failure(ErrDisabled, "仅主 Agent 可以控制桌面")
	}
	if s.AccessMode == "read-only" || s.AccessMode == "read_only" {
		return failure(ErrDisabled, "只读会话不能使用 Computer Use")
	}
	if s.Model == nil || !contains(s.Model.Input, "image") {
		return failure(ErrRequiresVisionModel, "Computer Use 需要支持图片输入的模型")
	}
	return nil
}
func contains[T comparable](values []T, v T) bool {
	for _, x := range values {
		if x == v {
			return true
		}
	}
	return false
}
func (h *Host) providerID(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if h.config.DefaultProviderID != "" {
		return h.config.DefaultProviderID
	}
	if h.config.Platform != "darwin" && h.config.Platform != "windows" {
		return "unsupported:" + h.config.Platform
	}
	if id := h.settings.ProviderByPlatform[h.config.Platform]; id != "" {
		if _, err := h.providers.Require(id); err == nil {
			return id
		}
	}
	if h.config.Platform == "darwin" {
		return "macos:ax"
	}
	return "windows:uia"
}

func (h *Host) GetStatus(ctx context.Context) (map[string]any, error) {
	op, done, err := h.enter(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	providers := make([]map[string]any, 0)
	for _, p := range h.providers.List() {
		s, e := p.GetStatus(op)
		if e != nil {
			if op.Err() != nil {
				return nil, op.Err()
			}
			s = ProviderStatus{Reason: e.Error()}
		}
		providers = append(providers, map[string]any{"providerId": p.ID(), "capabilities": p.Capabilities(), "status": s})
	}
	return map[string]any{
		"enabled":            h.settings.Enabled,
		"platform":           h.config.Platform,
		"selectedProviderId": h.providerID(""),
		"providers":          providers,
		"activeLease":        h.leases.Active(),
	}, nil
}
func (h *Host) ListApps(ctx context.Context, providerID string) ([]App, error) {
	op, done, err := h.enter(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	if err = h.assertRuntime(h.session(ctx)); err != nil {
		return nil, err
	}
	p, err := h.providers.Require(h.providerID(providerID))
	if err != nil {
		return nil, err
	}
	return p.ListApps(op)
}
func (h *Host) RequestPermissions(ctx context.Context, providerID string) (ProviderStatus, error) {
	op, done, err := h.enter(ctx)
	if err != nil {
		return ProviderStatus{}, err
	}
	defer done()
	p, err := h.providers.Require(h.providerID(providerID))
	if err != nil {
		return ProviderStatus{}, err
	}
	return p.RequestPermissions(op)
}

func (h *Host) CreateLease(ctx context.Context, target Target) (Lease, error) {
	op, done, err := h.enter(ctx)
	if err != nil {
		return Lease{}, err
	}
	defer done()
	s := h.session(ctx)
	if err = h.assertRuntime(s); err != nil {
		return Lease{}, err
	}
	id := h.providerID(target.ProviderID)
	p, err := h.providers.Require(id)
	if err != nil {
		return Lease{}, err
	}
	if target.AppID == "" && target.PID == 0 && target.Name != "" {
		apps, e := p.ListApps(op)
		if e != nil {
			return Lease{}, e
		}
		app := findAppByName(apps, target.Name)
		if app == nil {
			return Lease{}, failure(ErrTargetNotFound, "无法唯一确定应用名称", map[string]any{"appName": target.Name})
		}
		target.AppID = app.AppID
		target.Name = app.Name
		if target.WindowID == "" && len(app.Windows) > 0 {
			target.WindowID = app.Windows[0].WindowID
		}
	}
	appID := target.AppID
	if appID == "" && target.PID > 0 {
		appID = fmt.Sprintf("pid:%d", target.PID)
	}
	if appID == "" {
		appID = target.Name
	}
	if appID == "" {
		return Lease{}, failure(ErrInvalidArgument, "start 需要 appId、appName 或原生目标")
	}
	active := h.leases.Active()
	if active != nil && sameOwner(*active, s) && active.ProviderID == id && active.AppID == appID && (target.WindowID == "" || active.WindowID == target.WindowID) {
		if l, e := h.leases.Require(s, active.LeaseID); e == nil {
			return l, nil
		}
	}
	// 在释放旧租约前完成新目标授权，避免未批准的请求中断已有任务。
	if !p.Capabilities().Isolated && !approved(h.settings, id, appID) {
		if err = h.authorize(op, s, target, id, appID); err != nil {
			return Lease{}, err
		}
	}
	if active != nil {
		_ = h.release(op, *active)
	}
	if err = op.Err(); err != nil {
		return Lease{}, err
	}
	pl, err := p.CreateLease(op, target)
	if err != nil {
		return Lease{}, err
	}
	if pl.AppID == "" {
		pl.AppID = appID
	}
	if len(pl.AllowedActions) == 0 {
		pl.AllowedActions = []Action{ActionClickElement, ActionTypeText, ActionPressKey, ActionScroll, ActionSecondary, ActionStop}
	}
	if _, err := json.Marshal(pl); err != nil {
		return Lease{}, failure(ErrProviderCrashed, "Provider 租约状态不能编码为 JSON: "+err.Error())
	}
	return h.leases.Create(s, id, pl, h.config.LeaseTTL), nil
}
func (h *Host) authorize(ctx context.Context, s Session, target Target, id, appID string) error {
	if h.config.PermissionMode == PermissionOperate {
		return nil
	}
	a := AppApproval{ProviderID: id, AppID: appID, AppName: target.Name, Scope: "app", ApprovedAt: time.Now()}
	req := ApprovalRequest{Approval: a, Session: s, Target: target}
	decision := ApprovalDecision{Status: "ask_user"}
	var err error
	if h.config.PermissionMode == PermissionAuto && h.config.ReviewApp != nil {
		decision, err = h.config.ReviewApp(ctx, req)
		if err != nil {
			return err
		}
	}
	if decision.Status == "ask_user" && (s.AllowHumanApproval == nil || *s.AllowHumanApproval) && h.config.ConfirmApp != nil {
		decision, err = h.config.ConfirmApp(ctx, req)
		if err != nil {
			return err
		}
	}
	if decision.Status != "approved" {
		return failure(ErrAppApprovalRequired, "应用控制尚未获批", map[string]any{"providerId": id, "appId": appID, "confirmation": decision})
	}
	if decision.Scope == "app" {
		h.settings.AppApprovals = append(h.settings.AppApprovals, a)
	}
	return nil
}
func (h *Host) resolveLease(s Session, id string) (Lease, error) {
	if id == "" {
		if l := h.leases.LastFor(s); l != nil {
			id = l.LeaseID
		}
	}
	return h.leases.Require(s, id)
}
func (h *Host) GetActiveLease(ctx context.Context) (*Lease, error) {
	_, done, err := h.enter(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	l := h.leases.LastFor(h.session(ctx))
	if l == nil {
		return nil, nil
	}
	current, err := h.leases.Require(h.session(ctx), l.LeaseID)
	if err != nil {
		return nil, err
	}
	return &current, nil
}
func (h *Host) GetAppState(ctx context.Context, leaseID string) (Snapshot, error) {
	op, done, err := h.enter(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	defer done()
	s := h.session(ctx)
	if err = h.assertRuntime(s); err != nil {
		return Snapshot{}, err
	}
	l, err := h.resolveLease(s, leaseID)
	if err != nil {
		return Snapshot{}, err
	}
	p, err := h.providers.Require(l.ProviderID)
	if err != nil {
		return Snapshot{}, err
	}
	// 即便观察失败，原生驱动也可能已替换元素缓存，旧版本不能继续用于点击。
	h.leases.Invalidate(l.LeaseID)
	snapshot, err := p.GetAppState(op, l)
	if err != nil {
		return Snapshot{}, err
	}
	if snapshot.Screenshot.Data == "" {
		return Snapshot{}, failure(ErrProviderCrashed, "Provider 没有返回窗口截图")
	}
	if base64.StdEncoding.DecodedLen(len(snapshot.Screenshot.Data)) > h.config.MaxSnapshotBytes {
		return Snapshot{}, failure(ErrProviderCrashed, "窗口截图超过大小限制")
	}
	if _, err = base64.StdEncoding.DecodeString(snapshot.Screenshot.Data); err != nil {
		return Snapshot{}, failure(ErrProviderCrashed, "窗口截图不是有效 Base64")
	}
	snapshot.ProviderID = l.ProviderID
	snapshot.AppID = l.AppID
	if snapshot.WindowID == "" {
		snapshot.WindowID = l.WindowID
	}
	snapshot.Mode = "vision-native"
	snapshot.AllowedActions = l.AllowedActions
	snapshot.ActionCapabilities = p.Capabilities()
	if _, err := json.Marshal(snapshot); err != nil {
		return Snapshot{}, failure(ErrProviderCrashed, "Provider 快照不能编码为 JSON: "+err.Error())
	}
	return h.leases.Record(s, l.LeaseID, snapshot)
}
func (h *Host) PerformAction(ctx context.Context, leaseID string, action ResolvedAction) (ActionResult, error) {
	op, done, err := h.enter(ctx)
	if err != nil {
		return ActionResult{}, err
	}
	defer done()
	s := h.session(ctx)
	if err = h.assertRuntime(s); err != nil {
		return ActionResult{}, err
	}
	l, err := h.resolveLease(s, leaseID)
	if err != nil {
		return ActionResult{}, err
	}
	if action.SnapshotID == "" {
		action.SnapshotID = l.LastSnapshotID
	}
	snapshot, err := h.leases.Validate(s, l.LeaseID, action.SnapshotID)
	if err != nil {
		return ActionResult{}, err
	}
	if h.config.SnapshotTTL > 0 && time.Since(snapshot.CapturedAt) > h.config.SnapshotTTL {
		return ActionResult{}, failure(ErrStaleSnapshot, "快照已经过期，请重新观察")
	}
	if !contains(l.AllowedActions, action.Type) {
		return ActionResult{}, failure(ErrActionBlocked, "租约不允许该动作", map[string]any{"allowedActions": l.AllowedActions})
	}
	p, err := h.providers.Require(l.ProviderID)
	if err != nil {
		return ActionResult{}, err
	}
	if err = assertCapability(p.Capabilities(), action); err != nil {
		return ActionResult{}, err
	}
	action.SnapshotElement = nil
	action.SnapshotDisplay = &snapshot.Display
	if action.ElementID != "" {
		for _, el := range snapshot.Elements {
			if el.ElementID == action.ElementID {
				e := el
				action.SnapshotElement = &e
				break
			}
		}
		if action.SnapshotElement == nil {
			return ActionResult{}, failure(ErrTargetNotFound, "当前快照没有这个元素")
		}
		if !action.SnapshotElement.Enabled {
			return ActionResult{}, failure(ErrActionBlocked, "目标元素已禁用")
		}
	}
	result, err := p.PerformAction(op, l, action)
	if !h.config.PreserveSnapshotAfterAction {
		h.leases.Invalidate(l.LeaseID)
	}
	return result, err
}
func assertCapability(c Capabilities, a ResolvedAction) error {
	var value string
	switch a.Type {
	case ActionClickElement, ActionTypeText, ActionScroll, ActionSecondary:
		if c.ElementActions {
			return nil
		}
	case ActionPressKey:
		value = c.KeyboardInput
	case ActionClickPoint:
		value = c.PointClick
	case ActionDrag:
		value = c.Drag
	case ActionDoubleClick:
		if a.ElementID != "" && c.ElementDoubleClick {
			return nil
		}
		value = c.PointClick
	default:
		return failure(ErrCapabilityUnsupported, "未知动作")
	}
	switch value {
	case "allowed", "semantic", "focused", "pidScoped":
		return nil
	case "foreground":
		return failure(ErrRequiresForeground, "该动作需要前台输入")
	case "requiresApproval":
		return failure(ErrInputApprovalRequired, "该动作需要输入注入批准")
	}
	return failure(ErrCapabilityUnsupported, "Provider 不支持该动作")
}
func (h *Host) release(ctx context.Context, l Lease) error {
	h.leases.Release(l.LeaseID)
	p, err := h.providers.Require(l.ProviderID)
	if err != nil {
		return err
	}
	return errors.Join(p.Stop(ctx, l), p.ReleaseLease(ctx, l))
}
func (h *Host) Stop(ctx context.Context, leaseID string) error {
	s := h.session(ctx)
	h.cancelCurrent(&s)
	op, done, err := h.enter(ctx)
	if err != nil {
		return err
	}
	defer done()
	l := h.leases.LastFor(s)
	if l == nil {
		return nil
	}
	if leaseID != "" {
		v, e := h.leases.Require(s, leaseID)
		if e != nil {
			return e
		}
		l = &v
	}
	if l.Status != "active" {
		return nil
	}
	return h.release(op, *l)
}
func (h *Host) AbortSession(ctx context.Context) error { return h.Stop(ctx, "") }
func (h *Host) UpdateSettings(ctx context.Context, settings Settings) error {
	op, done, err := h.enter(ctx)
	if err != nil {
		return err
	}
	defer done()
	h.settings = normalizeSettings(settings)
	if l := h.leases.Active(); l != nil {
		p, e := h.providers.Require(l.ProviderID)
		if e != nil || !h.settings.Enabled || (!p.Capabilities().Isolated && h.config.PermissionMode != PermissionOperate && !approved(h.settings, l.ProviderID, l.AppID)) {
			return h.release(op, *l)
		}
	}
	return nil
}
func (h *Host) ApproveApp(ctx context.Context, a AppApproval) error {
	_, done, err := h.enter(ctx)
	if err != nil {
		return err
	}
	defer done()
	if a.ProviderID == "" || a.AppID == "" {
		return failure(ErrInvalidArgument, "应用批准需要 providerId 和 appId")
	}
	if a.ApprovedAt.IsZero() {
		a.ApprovedAt = time.Now()
	}
	if !approved(h.settings, a.ProviderID, a.AppID) {
		h.settings.AppApprovals = append(h.settings.AppApprovals, a)
	}
	return nil
}
func (h *Host) RevokeApp(ctx context.Context, providerID, appID string) error {
	op, done, err := h.enter(ctx)
	if err != nil {
		return err
	}
	defer done()
	out := h.settings.AppApprovals[:0]
	for _, a := range h.settings.AppApprovals {
		if a.ProviderID != providerID || a.AppID != appID {
			out = append(out, a)
		}
	}
	h.settings.AppApprovals = out
	if l := h.leases.Active(); l != nil && l.ProviderID == providerID && l.AppID == appID {
		return h.release(op, *l)
	}
	return nil
}
func (h *Host) Dispose(ctx context.Context) error {
	h.cancelCurrent(nil)
	select {
	case h.gate <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-h.gate }()
	if h.closed {
		return nil
	}
	h.closed = true
	var errs []error
	if l := h.leases.Active(); l != nil {
		errs = append(errs, h.release(ctx, *l))
	}
	for _, p := range h.providers.List() {
		errs = append(errs, p.Dispose(ctx))
	}
	return errors.Join(errs...)
}

func findAppByName(apps []App, name string) *App {
	query := strings.ToLower(strings.TrimSpace(name))
	best := -1
	score := 0
	ambiguous := false
	for i, a := range apps {
		current := 0
		for _, v := range []string{a.Name, a.AppID} {
			v = strings.ToLower(v)
			n := 0
			switch {
			case v == query:
				n = 100
			case strings.HasSuffix(v, "."+query):
				n = 90
			case v != "" && (strings.Contains(v, query) || strings.Contains(query, v)):
				n = 80
			}
			if n > current {
				current = n
			}
		}
		if current > score {
			best = i
			score = current
			ambiguous = false
		} else if current > 0 && current == score {
			ambiguous = true
		}
	}
	if best < 0 || ambiguous {
		return nil
	}
	return &apps[best]
}

// ProviderRegistry 支持注入自定义 Provider，重复 ID 会立即返回错误。
type ProviderRegistry struct {
	mu        sync.RWMutex
	providers map[string]Provider
	order     []string
}

func NewProviderRegistry() *ProviderRegistry {
	return &ProviderRegistry{providers: make(map[string]Provider)}
}
func (r *ProviderRegistry) Register(p Provider) error {
	if p == nil || p.ID() == "" {
		return fmt.Errorf("provider 必须声明 ID")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.providers[p.ID()]; ok {
		return fmt.Errorf("provider 重复: %s", p.ID())
	}
	r.providers[p.ID()] = p
	r.order = append(r.order, p.ID())
	return nil
}
func (r *ProviderRegistry) Require(id string) (Provider, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[id]
	if !ok {
		return nil, failure(ErrProviderUnavailable, "Provider 不可用", map[string]any{"providerId": id})
	}
	return p, nil
}
func (r *ProviderRegistry) List() []Provider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Provider, 0, len(r.order))
	for _, id := range r.order {
		out = append(out, r.providers[id])
	}
	return out
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func clone[T any](value T) T {
	b, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("内部状态必须能够序列化: %v", err))
	}
	var out T
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	if err = decoder.Decode(&out); err != nil {
		panic(err)
	}
	return out
}
func sameOwner(l Lease, s Session) bool {
	if l.AgentID != s.AgentID {
		return false
	}
	if l.SessionID != "" && s.ID != "" {
		return l.SessionID == s.ID
	}
	return s.Path != "" && l.SessionPath == s.Path
}

// LeaseRegistry 仅保留每个租约最新的快照，避免历史截图持续占用内存。
// Host 的执行队列负责完整操作的互斥，这里的锁只保护注册表本身。
type LeaseRegistry struct {
	mu        sync.RWMutex
	leases    map[string]Lease
	order     []string
	snapshots map[string]Snapshot
	now       func() time.Time
}

func NewLeaseRegistry() *LeaseRegistry {
	return &LeaseRegistry{leases: map[string]Lease{}, snapshots: map[string]Snapshot{}, now: time.Now}
}
func (r *LeaseRegistry) Create(s Session, providerID string, p ProviderLease, ttl time.Duration) Lease {
	r.mu.Lock()
	defer r.mu.Unlock()
	l := Lease{
		ProviderLease: clone(p),
		LeaseID:       newID(),
		ProviderID:    providerID,
		SessionID:     s.ID,
		SessionPath:   s.Path,
		AgentID:       s.AgentID,
		CreatedAt:     r.now(),
		Status:        "active",
	}
	if ttl > 0 {
		t := r.now().Add(ttl)
		l.ExpiresAt = &t
	}
	r.leases[l.LeaseID] = l
	r.order = append(r.order, l.LeaseID)
	return clone(l)
}
func (r *LeaseRegistry) Active() *Lease {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, id := range r.order {
		l := r.leases[id]
		if l.Status == "active" {
			c := clone(l)
			return &c
		}
	}
	return nil
}
func (r *LeaseRegistry) LastFor(s Session) *Lease {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for i := len(r.order) - 1; i >= 0; i-- {
		l := r.leases[r.order[i]]
		if sameOwner(l, s) {
			c := clone(l)
			return &c
		}
	}
	return nil
}
func (r *LeaseRegistry) Require(s Session, id string) (Lease, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.requireLocked(s, id)
}

func (r *LeaseRegistry) requireLocked(s Session, id string) (Lease, error) {
	l, ok := r.leases[id]
	if !ok || !sameOwner(l, s) {
		return Lease{}, failure(ErrLeaseNotFound, "当前会话没有此租约")
	}
	if l.Status != "active" || (l.ExpiresAt != nil && !r.now().Before(*l.ExpiresAt)) {
		return Lease{}, failure(ErrLeaseReleased, "租约已释放或过期")
	}
	return clone(l), nil
}
func (r *LeaseRegistry) Release(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if l, ok := r.leases[id]; ok {
		l.Status = "released"
		l.LastSnapshotID = ""
		r.leases[id] = l
	}
	delete(r.snapshots, id)
}

// Invalidate 在一次原生动作后丢弃旧的元素版本；发生超时也可能已经有副作用。
func (r *LeaseRegistry) Invalidate(id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.snapshots, id)
	if l, ok := r.leases[id]; ok {
		l.LastSnapshotID = ""
		r.leases[id] = l
	}
}
func (r *LeaseRegistry) Record(s Session, id string, snapshot Snapshot) (Snapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, err := r.requireLocked(s, id); err != nil {
		return Snapshot{}, err
	}
	snapshot.SnapshotID = newID()
	snapshot.LeaseID = id
	snapshot.CapturedAt = r.now()
	r.snapshots[id] = clone(snapshot)
	l := r.leases[id]
	l.LastSnapshotID = snapshot.SnapshotID
	r.leases[id] = l
	return clone(snapshot), nil
}
func (r *LeaseRegistry) Validate(s Session, id, snapshotID string) (Snapshot, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if _, err := r.requireLocked(s, id); err != nil {
		return Snapshot{}, err
	}
	snapshot, ok := r.snapshots[id]
	if !ok || snapshotID == "" || snapshot.SnapshotID != snapshotID {
		return Snapshot{}, failure(ErrStaleSnapshot, "快照已失效，请重新 get_app_state")
	}
	return clone(snapshot), nil
}

// DefaultSettings 返回默认设置；真实桌面控制由宿主明确开启。
func DefaultSettings() Settings {
	return Settings{ProviderByPlatform: map[string]string{"darwin": "macos:ax", "windows": "windows:uia", "linux": "mock"}}
}
func normalizeSettings(s Settings) Settings {
	out := DefaultSettings()
	out.Enabled = s.Enabled
	for k, v := range s.ProviderByPlatform {
		if k == "win32" {
			k = "windows"
		}
		if v != "" {
			out.ProviderByPlatform[k] = v
		}
	}
	out.AppApprovals = append([]AppApproval(nil), s.AppApprovals...)
	return out
}
func approved(s Settings, providerID, appID string) bool {
	for _, a := range s.AppApprovals {
		if a.ProviderID == providerID && a.AppID == appID {
			return true
		}
	}
	return false
}
