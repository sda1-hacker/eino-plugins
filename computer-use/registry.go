package computeruse

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

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

// DefaultSettings 与 OpenHanako 一致，真实桌面控制默认关闭。
func DefaultSettings() Settings {
	return Settings{ProviderByPlatform: map[string]string{"darwin": "macos:cua", "windows": "windows:uia", "linux": "mock"}}
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
