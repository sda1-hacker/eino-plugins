package computeruse

import (
	"context"
	"time"
)

// Action 表示 computer 工具的动作；像素动作只用于返回兼容的能力错误。
type Action string

const (
	ActionStatus       Action = "status"
	ActionListApps     Action = "list_apps"
	ActionStart        Action = "start"
	ActionGetAppState  Action = "get_app_state"
	ActionClickElement Action = "click_element"
	ActionTypeText     Action = "type_text"
	ActionPressKey     Action = "press_key"
	ActionScroll       Action = "scroll"
	ActionSecondary    Action = "perform_secondary_action"
	ActionStop         Action = "stop"
	ActionClickPoint   Action = "click_point"
	ActionDoubleClick  Action = "double_click"
	ActionDrag         Action = "drag"
)

var modelActions = []Action{
	ActionStatus,
	ActionListApps,
	ActionStart,
	ActionGetAppState,
	ActionClickElement,
	ActionTypeText,
	ActionPressKey,
	ActionScroll,
	ActionSecondary,
	ActionStop,
}
var elementActions = []Action{ActionClickElement, ActionTypeText, ActionScroll, ActionSecondary}

// Param 与 browseruse.Param 一样使用 action 分发；字段名兼容 OpenHanako。
type Param struct {
	Action     Action  `json:"action"`
	ProviderID string  `json:"providerId,omitempty"`
	LeaseID    string  `json:"leaseId,omitempty"`
	SnapshotID string  `json:"snapshotId,omitempty"`
	AppID      string  `json:"appId,omitempty"`
	AppName    string  `json:"appName,omitempty"`
	WindowID   string  `json:"windowId,omitempty"`
	ElementID  string  `json:"elementId,omitempty"`
	Text       *string `json:"text,omitempty"`
	Key        string  `json:"key,omitempty"`
	Direction  string  `json:"direction,omitempty"`
	Amount     int     `json:"amount,omitempty"`
}

// Bounds 使用平台原始坐标；显示缩放信息独立存储，避免混淆截图像素和屏幕点。
type Bounds struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}
type Display struct {
	Bounds
	OriginalWidth  float64 `json:"originalWidth,omitempty"`
	OriginalHeight float64 `json:"originalHeight,omitempty"`
	ScaleFactor    float64 `json:"scaleFactor"`
	ScreenBounds   *Bounds `json:"screenBounds,omitempty"`
}

// Image.Data 为图片 Base64，不能作为普通文本替代模型的图片输入。
type Image struct {
	Type     string `json:"type"`
	MIMEType string `json:"mimeType"`
	Data     string `json:"data"`
}
type Element struct {
	ElementID          string   `json:"elementId"`
	Role               string   `json:"role"`
	Label              string   `json:"label"`
	Value              any      `json:"value,omitempty"`
	Enabled            bool     `json:"enabled"`
	Bounds             *Bounds  `json:"bounds,omitempty"`
	Actions            []string `json:"actions,omitempty"`
	Patterns           []string `json:"patterns,omitempty"`
	AutomationID       string   `json:"automationId,omitempty"`
	NativeWindowHandle int64    `json:"nativeWindowHandle,omitempty"`
}
type Window struct {
	WindowID       string  `json:"windowId"`
	Title          string  `json:"title"`
	Bounds         *Bounds `json:"bounds,omitempty"`
	IsOnScreen     *bool   `json:"isOnScreen,omitempty"`
	OnCurrentSpace *bool   `json:"onCurrentSpace,omitempty"`
}
type App struct {
	AppID        string         `json:"appId"`
	Name         string         `json:"name"`
	PID          int            `json:"pid,omitempty"`
	Active       bool           `json:"active,omitempty"`
	Windows      []Window       `json:"windows"`
	ProviderData map[string]any `json:"providerData,omitempty"`
}
type Target struct {
	ProviderID string `json:"providerId,omitempty"`
	AppID      string `json:"appId,omitempty"`
	Name       string `json:"name,omitempty"`
	PID        int    `json:"pid,omitempty"`
	WindowID   string `json:"windowId,omitempty"`
}

// Capabilities 保留原项目的能力声明；不支持的前台输入不会被悄悄降级执行。
type Capabilities struct {
	Platform                   string   `json:"platform"`
	ObservationModes           []string `json:"observationModes"`
	Screenshot                 bool     `json:"screenshot"`
	AccessibilityTree          bool     `json:"accessibilityTree"`
	ElementActions             bool     `json:"elementActions"`
	ElementDoubleClick         bool     `json:"elementDoubleClick"`
	BackgroundControl          string   `json:"backgroundControl"`
	PointClick                 string   `json:"pointClick"`
	Drag                       string   `json:"drag"`
	TextInput                  string   `json:"textInput"`
	KeyboardInput              string   `json:"keyboardInput"`
	RequiresForegroundForInput bool     `json:"requiresForegroundForInput"`
	NativeCursor               bool     `json:"nativeCursor"`
	Isolated                   bool     `json:"isolated"`
}
type Permission struct {
	Name    string `json:"name"`
	Granted bool   `json:"granted"`
}
type ProviderStatus struct {
	Available   bool           `json:"available"`
	Reason      string         `json:"reason,omitempty"`
	Permissions []Permission   `json:"permissions,omitempty"`
	Details     map[string]any `json:"details,omitempty"`
}
type ProviderLease struct {
	AppID          string         `json:"appId"`
	WindowID       string         `json:"windowId,omitempty"`
	AllowedActions []Action       `json:"allowedActions"`
	State          map[string]any `json:"providerState,omitempty"`
}

// Lease 属于宿主会话，不允许模型自行指定 owner。
type Lease struct {
	ProviderLease
	LeaseID        string     `json:"leaseId"`
	ProviderID     string     `json:"providerId"`
	SessionID      string     `json:"sessionId,omitempty"`
	SessionPath    string     `json:"sessionPath,omitempty"`
	AgentID        string     `json:"agentId,omitempty"`
	CreatedAt      time.Time  `json:"createdAt"`
	ExpiresAt      *time.Time `json:"expiresAt,omitempty"`
	Status         string     `json:"status"`
	LastSnapshotID string     `json:"lastSnapshotId,omitempty"`
}
type Snapshot struct {
	SnapshotID         string       `json:"snapshotId"`
	LeaseID            string       `json:"leaseId"`
	ProviderID         string       `json:"providerId"`
	AppID              string       `json:"appId"`
	WindowID           string       `json:"windowId"`
	Mode               string       `json:"mode"`
	CapturedAt         time.Time    `json:"capturedAt"`
	Screenshot         Image        `json:"screenshot"`
	Display            Display      `json:"display"`
	FocusedElementID   string       `json:"focusedElementId,omitempty"`
	Elements           []Element    `json:"elements"`
	AllowedActions     []Action     `json:"allowedActions"`
	ActionCapabilities Capabilities `json:"actionCapabilities"`
}

// ResolvedAction 的快照元数据由 Host 填充，不接受模型传入的定位元数据。
type ResolvedAction struct {
	Type            Action   `json:"type"`
	SnapshotID      string   `json:"snapshotId,omitempty"`
	ElementID       string   `json:"elementId,omitempty"`
	Text            string   `json:"text,omitempty"`
	Key             string   `json:"key,omitempty"`
	Direction       string   `json:"direction,omitempty"`
	Amount          int      `json:"amount,omitempty"`
	SnapshotElement *Element `json:"snapshotElement,omitempty"`
	SnapshotDisplay *Display `json:"snapshotDisplay,omitempty"`
}
type ActionResult struct {
	OK     bool           `json:"ok"`
	Mode   string         `json:"mode,omitempty"`
	Output string         `json:"output,omitempty"`
	Data   map[string]any `json:"data,omitempty"`
}

// Provider 把平台差异隔离在工具外；实现必须尊重 ctx 的取消和超时。
type Provider interface {
	ID() string
	Capabilities() Capabilities
	GetStatus(context.Context) (ProviderStatus, error)
	RequestPermissions(context.Context) (ProviderStatus, error)
	ListApps(context.Context) ([]App, error)
	CreateLease(context.Context, Target) (ProviderLease, error)
	GetAppState(context.Context, Lease) (Snapshot, error)
	PerformAction(context.Context, Lease, ResolvedAction) (ActionResult, error)
	ReleaseLease(context.Context, Lease) error
	Stop(context.Context, Lease) error
	Dispose(context.Context) error
}

type ModelInfo struct {
	ID       string   `json:"id,omitempty"`
	Provider string   `json:"provider,omitempty"`
	Input    []string `json:"input"`
}

// Session 必须由应用通过 WithSession 注入；不能放进模型参数 schema。
type Session struct {
	ID                 string
	Path               string
	AgentID            string
	AccessMode         string
	Model              *ModelInfo
	AllowHumanApproval *bool
}
type sessionKey struct{}

func WithSession(ctx context.Context, session Session) context.Context {
	return context.WithValue(ctx, sessionKey{}, session)
}

type PermissionMode string

const (
	PermissionAsk     PermissionMode = "ask"
	PermissionAuto    PermissionMode = "auto"
	PermissionOperate PermissionMode = "operate"
)

type AppApproval struct {
	ProviderID string    `json:"providerId"`
	AppID      string    `json:"appId"`
	AppName    string    `json:"appName,omitempty"`
	Scope      string    `json:"scope,omitempty"`
	ApprovedAt time.Time `json:"approvedAt"`
}
type Settings struct {
	Enabled            bool              `json:"enabled"`
	ProviderByPlatform map[string]string `json:"provider_by_platform"`
	AppApprovals       []AppApproval     `json:"app_approvals"`
}
type ApprovalRequest struct {
	Approval AppApproval
	Session  Session
	Target   Target
}

// ApprovalDecision.Status 可为 approved、ask_user、denied、blocked；Scope 为 session 或 app。
type ApprovalDecision struct {
	Status string `json:"status"`
	Scope  string `json:"scope,omitempty"`
	Reason string `json:"reason,omitempty"`
}
type ApprovalHandler func(context.Context, ApprovalRequest) (ApprovalDecision, error)
type Event struct {
	Phase         string    `json:"phase"`
	Action        Action    `json:"action"`
	SessionID     string    `json:"sessionId,omitempty"`
	LeaseID       string    `json:"leaseId,omitempty"`
	SnapshotID    string    `json:"snapshotId,omitempty"`
	VisualSurface string    `json:"visualSurface,omitempty"`
	ErrorCode     ErrorCode `json:"errorCode,omitempty"`
	At            time.Time `json:"at"`
}

// ToolResult 沿用 browseruse 的 Output/Error/Base64Image 风格，另保留结构化详情。
type ToolResult struct {
	Output      string         `json:"output,omitempty"`
	Error       string         `json:"error,omitempty"`
	ErrorCode   ErrorCode      `json:"errorCode,omitempty"`
	Base64Image string         `json:"base64_image,omitempty"`
	MIMEType    string         `json:"mime_type,omitempty"`
	Details     map[string]any `json:"details,omitempty"`
}
