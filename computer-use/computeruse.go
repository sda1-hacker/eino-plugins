package computeruse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

const toolName = "computer"
const toolDescription = `通过截图和无障碍元素检查、控制已经批准的桌面应用。
先 list_apps，再 start(appId)，然后 get_app_state 获取截图、snapshotId 和 elementId。
只使用最新快照中的元素编号进行 click_element、type_text、scroll、perform_secondary_action；macOS 也支持 press_key。
动作后重新 get_app_state 确认效果。需要图片输入模型。坐标点击、双击、拖拽不可用。
找不到元素时报告能力限制，完成后调用 stop 释放资源。`

// Tool 遵循 browseruse 的 Config / Param / Execute / Cleanup 风格。
// Eino 多模态接口与字符串接口的方法同名但签名不同，因此旧接口通过 AsTextTool 适配。
type Tool struct {
	host     *Host
	ctx      context.Context
	config   Config
	ownsHost bool
}

var _ tool.EnhancedInvokableTool = (*Tool)(nil)

// NewComputerUseTool 创建工具；构造阶段不启动原生进程或触发系统权限弹窗。
func NewComputerUseTool(ctx context.Context, config *Config) (*Tool, error) {
	if ctx == nil {
		return nil, fmt.Errorf("ctx 不能为空")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c := Config{}
	if config != nil {
		c = *config
	}
	h := c.Host
	owned := false
	if h == nil {
		var err error
		h, err = NewHost(&c)
		if err != nil {
			return nil, err
		}
		owned = true
	}
	return &Tool{host: h, ctx: ctx, config: c, ownsHost: owned}, nil
}
func toolInfo() *schema.ToolInfo {
	actions := make([]string, 0, len(modelActions))
	for _, a := range modelActions {
		actions = append(actions, string(a))
	}
	return &schema.ToolInfo{Name: toolName, Desc: toolDescription, ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
		"action":     {Type: schema.String, Required: true, Enum: actions, Desc: "要执行的桌面操作"},
		"providerId": {Type: schema.String, Desc: "Provider ID，通常省略以使用平台默认值"},
		"leaseId":    {Type: schema.String, Desc: "start 返回的控制租约；省略时使用当前会话租约"},
		"snapshotId": {Type: schema.String, Desc: "最近 get_app_state 返回的快照 ID"},
		"appId":      {Type: schema.String, Desc: "start 的应用标识，例如 com.apple.TextEdit 或 pid:123"},
		"appName":    {Type: schema.String, Desc: "start 的应用名称，需能够唯一匹配 list_apps 的结果"},
		"windowId":   {Type: schema.String, Desc: "list_apps 返回的原生窗口编号"},
		"elementId":  {Type: schema.String, Desc: "当前快照中的元素编号，不可自行猜测"},
		"text":       {Type: schema.String, Desc: "type_text 输入的文字"},
		"key":        {Type: schema.String, Desc: "press_key 的按键名称，例如 return、tab、escape"},
		"direction":  {Type: schema.String, Enum: []string{"up", "down", "left", "right"}, Desc: "scroll 的方向"},
		"amount":     {Type: schema.Integer, Desc: "scroll 的滚动次数，默认 3，范围 1～50"},
	})}
}

// Info 返回中文工具说明和参数 schema，每次返回独立对象以避免外部修改。
func (t *Tool) Info(context.Context) (*schema.ToolInfo, error) { return toolInfo(), nil }

// Host 暴露宿主侧管理 API，例如应用审批和系统权限请求。
func (t *Tool) Host() *Host { return t.host }

// Execute 使用构造时的上下文，保持 browseruse 风格的直接调用入口。
func (t *Tool) Execute(params *Param) (*ToolResult, error) { return t.ExecuteContext(t.ctx, params) }

// ExecuteContext 保留业务错误作为工具结果，取消或超时则交给 Eino 中止当前调用。
func (t *Tool) ExecuteContext(ctx context.Context, params *Param) (result *ToolResult, err error) {
	if ctx == nil {
		return nil, fmt.Errorf("ctx 不能为空")
	}
	op, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(t.ctx, cancel)
	defer stop()
	defer cancel()
	if err = op.Err(); err != nil {
		return nil, err
	}
	if err = t.ctx.Err(); err != nil {
		return nil, err
	}
	if params == nil {
		return errorResult(failure(ErrInvalidArgument, "params 不能为空")), nil
	}
	p := *params
	if e := validateParam(p); e != nil {
		return errorResult(e), nil
	}
	if contains(elementActions, p.Action) || p.Action == ActionPressKey {
		t.emit(op, "preview", p, "")
	}
	t.emit(op, "running", p, "")
	defer func() {
		if err != nil {
			t.emit(context.WithoutCancel(op), "error", p, ErrProviderCrashed)
		} else if result != nil && result.ErrorCode != "" {
			t.emit(op, "error", p, result.ErrorCode)
		} else {
			phase := "done"
			if p.Action == ActionStop {
				phase = "clear"
			}
			t.emit(op, phase, p, "")
		}
	}()
	result, err = t.execute(op, p)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}
		return errorResult(err), nil
	}
	return result, nil
}
func validateParam(p Param) error {
	if !contains(modelActions, p.Action) {
		return failure(ErrCapabilityUnsupported, "此动作没有暴露给模型")
	}
	switch p.Action {
	case ActionStart:
		if p.AppID == "" && strings.TrimSpace(p.AppName) == "" {
			return failure(ErrInvalidArgument, "start 需要 appId 或 appName")
		}
	case ActionClickElement, ActionSecondary:
		if p.ElementID == "" {
			return failure(ErrInvalidArgument, "元素动作需要 elementId")
		}
	case ActionTypeText:
		if p.Text == nil {
			return failure(ErrInvalidArgument, "type_text 需要 text")
		}
	case ActionPressKey:
		if strings.TrimSpace(p.Key) == "" {
			return failure(ErrInvalidArgument, "press_key 需要 key")
		}
	case ActionScroll:
		if !contains([]string{"up", "down", "left", "right"}, p.Direction) {
			return failure(ErrInvalidArgument, "scroll 需要有效 direction")
		}
		if p.Amount < 0 || p.Amount > 50 {
			return failure(ErrInvalidArgument, "amount 必须在 1～50 之间")
		}
	}
	return nil
}
func (t *Tool) execute(ctx context.Context, p Param) (*ToolResult, error) {
	switch p.Action {
	case ActionStatus:
		status, err := t.host.GetStatus(ctx)
		return &ToolResult{Output: "Computer Use 状态已加载", Details: status}, err
	case ActionListApps:
		apps, err := t.host.ListApps(ctx, p.ProviderID)
		return &ToolResult{Output: "应用列表已加载", Details: map[string]any{"apps": apps}}, err
	case ActionStart:
		l, err := t.host.CreateLease(ctx, Target{ProviderID: p.ProviderID, AppID: p.AppID, Name: p.AppName, WindowID: p.WindowID})
		return &ToolResult{Output: "控制租约已建立", Details: map[string]any{"lease": l}}, err
	case ActionGetAppState:
		s, err := t.host.GetAppState(ctx, p.LeaseID)
		if err != nil {
			return nil, err
		}
		return &ToolResult{
			Output:      stateText(s),
			Base64Image: s.Screenshot.Data,
			MIMEType:    s.Screenshot.MIMEType,
			Details: map[string]any{
				"snapshotId":         s.SnapshotID,
				"leaseId":            s.LeaseID,
				"appId":              s.AppID,
				"windowId":           s.WindowID,
				"providerId":         s.ProviderID,
				"allowedActions":     s.AllowedActions,
				"actionCapabilities": s.ActionCapabilities,
				"elements":           s.Elements,
				"accessibilityTree":  s.AccessibilityTree,
				"display":            s.Display,
			},
		}, nil
	case ActionStop:
		err := t.host.Stop(ctx, p.LeaseID)
		return &ToolResult{Output: "Computer Use 已停止"}, err
	default:
		a := ResolvedAction{
			Type:       p.Action,
			SnapshotID: p.SnapshotID,
			ElementID:  p.ElementID,
			Key:        p.Key,
			Direction:  p.Direction,
			Amount:     p.Amount,
		}
		if p.Text != nil {
			a.Text = *p.Text
		}
		r, err := t.host.PerformAction(ctx, p.LeaseID, a)
		return &ToolResult{
			Output:  firstString(r.Output, "动作已执行，请重新 get_app_state 确认结果"),
			Details: map[string]any{"action": p.Action, "result": r},
		}, err
	}
}
func stateText(s Snapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "当前应用状态：\nleaseId: %s\nsnapshotId: %s\nappId: %s\nwindowId: %s\n使用这些元素编号操作；动作后重新获取状态。\n", s.LeaseID, s.SnapshotID, s.AppID, s.WindowID)
	for i, e := range s.Elements {
		if i >= 60 {
			fmt.Fprintf(&b, "... 还有 %d 个元素，见结构化 elements。\n", len(s.Elements)-60)
			break
		}
		fmt.Fprintf(&b, "- %s: %s %q", e.ElementID, e.Role, e.Label)
		if e.Value != nil {
			fmt.Fprintf(&b, " value=%v", e.Value)
		}
		if !e.Enabled {
			b.WriteString(" disabled")
		}
		if len(e.Actions) > 0 {
			fmt.Fprintf(&b, " actions=%v", e.Actions)
		}
		b.WriteByte('\n')
	}
	if len(s.Elements) == 0 {
		b.WriteString("该应用没有暴露可操作元素，请报告能力限制。\n")
	}
	return b.String()
}
func (t *Tool) emit(ctx context.Context, phase string, p Param, code ErrorCode) {
	if t.config.OnEvent == nil {
		return
	}
	s := t.host.session(ctx)
	surface := "renderer"
	runtimeID := p.ProviderID
	if runtimeID == "" {
		runtimeID = t.host.config.DefaultProviderID
	}
	if runtimeID == "" {
		switch t.host.config.Platform {
		case "darwin":
			runtimeID = "macos:cua"
		case "windows":
			runtimeID = "windows:uia"
		}
	}
	if runtimeID != "" {
		if provider, e := t.host.providers.Require(runtimeID); e == nil && provider.Capabilities().NativeCursor {
			surface = "provider"
		}
	}
	t.config.OnEvent(ctx, Event{
		Phase:         phase,
		Action:        p.Action,
		SessionID:     s.ID,
		LeaseID:       p.LeaseID,
		SnapshotID:    p.SnapshotID,
		VisualSurface: surface,
		ErrorCode:     code,
		At:            time.Now(),
	})
}

func parseParam(text string) (*Param, error) {
	if len(text) > 1<<20 {
		return nil, failure(ErrInvalidArgument, "工具参数超过大小限制")
	}
	d := json.NewDecoder(strings.NewReader(text))
	d.DisallowUnknownFields()
	var p Param
	if err := d.Decode(&p); err != nil {
		return nil, failure(ErrInvalidArgument, "工具参数必须是有效 JSON: "+err.Error())
	}
	var trailing any
	if err := d.Decode(&trailing); err != io.EOF {
		return nil, failure(ErrInvalidArgument, "参数只允许一个 JSON 对象")
	}
	return &p, nil
}
func toEinoResult(result *ToolResult) (*schema.ToolResult, error) {
	text := *result
	text.Base64Image = ""
	text.MIMEType = ""
	b, err := json.Marshal(text)
	if err != nil {
		return nil, err
	}
	parts := []schema.ToolOutputPart{{Type: schema.ToolPartTypeText, Text: string(b)}}
	if result.Base64Image != "" {
		data := result.Base64Image
		parts = append(parts, schema.ToolOutputPart{
			Type:  schema.ToolPartTypeImage,
			Image: &schema.ToolOutputImage{MessagePartCommon: schema.MessagePartCommon{Base64Data: &data, MIMEType: result.MIMEType}},
		})
	}
	return &schema.ToolResult{Parts: parts}, nil
}
func (t *Tool) InvokableRun(ctx context.Context, argument *schema.ToolArgument, _ ...tool.Option) (*schema.ToolResult, error) {
	if argument == nil {
		return toEinoResult(errorResult(failure(ErrInvalidArgument, "工具参数不能为空")))
	}
	p, err := parseParam(argument.Text)
	if err != nil {
		return toEinoResult(errorResult(err))
	}
	r, err := t.ExecuteContext(ctx, p)
	if err != nil {
		return nil, err
	}
	return toEinoResult(r)
}

// TextTool 提供与 browseruse 完全相同的字符串 InvokableTool 接口。
// 使用此适配器时，宿主必须自行把 Base64Image 转成模型图片消息。
type TextTool struct{ tool *Tool }

var _ tool.InvokableTool = (*TextTool)(nil)

func (t *Tool) AsTextTool() *TextTool                                  { return &TextTool{tool: t} }
func (t *TextTool) Info(ctx context.Context) (*schema.ToolInfo, error) { return t.tool.Info(ctx) }
func (t *TextTool) InvokableRun(ctx context.Context, arguments string, _ ...tool.Option) (string, error) {
	p, err := parseParam(arguments)
	var r *ToolResult
	if err != nil {
		r = errorResult(err)
	} else {
		r, err = t.tool.ExecuteContext(ctx, p)
		if err != nil {
			return "", err
		}
	}
	b, err := json.Marshal(r)
	return string(b), err
}

// Cleanup 可以重复调用；共享 Host 的生命周期由创建它的应用负责。
func (t *Tool) Cleanup() error {
	if !t.ownsHost {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return t.host.Dispose(ctx)
}
