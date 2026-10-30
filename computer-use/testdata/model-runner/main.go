// 由 scripts/verify-native.mjs --model 复制到临时独立模块运行。
// 这里只使用验证窗口，不向模型暴露环境变量、文件工具或其他应用截图。
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
	computeruse "github.com/sda1-hacker/eino-plugins/computer-use"
)

const fixtureID = "local.eino.computeruse.validation"

type actionRecord struct {
	Action    computeruse.Action `json:"action"`
	Passed    bool               `json:"passed"`
	Error     string             `json:"error,omitempty"`
	Arguments string             `json:"arguments,omitempty"`
}
type modelReport struct {
	StartedAt  time.Time       `json:"startedAt"`
	Passed     bool            `json:"passed"`
	Model      string          `json:"model"`
	ModelCalls int             `json:"modelCalls"`
	Tokens     int             `json:"totalTokens"`
	Images     int             `json:"imageParts"`
	Actions    []actionRecord  `json:"actions"`
	Checks     map[string]bool `json:"checks"`
	Final      string          `json:"final,omitempty"`
	Reason     string          `json:"reason,omitempty"`
}

// 验证器在工具边界独立检查真实应用状态，不相信模型自行宣称“已完成”。
type auditedTool struct {
	*computeruse.Tool
	report    *modelReport
	code      string
	observed  bool
	directory string
}

func (t *auditedTool) Info(ctx context.Context) (*schema.ToolInfo, error) {
	info, err := t.Tool.Info(ctx)
	if err == nil {
		info.Desc = "只操作已运行的验证窗口 local.eino.computeruse.validation。直接 start(appId)，不要 list_apps。get_app_state 返回截图及 AX 树；用最新 elementId 输入和点击，每个动作后重新观察，最后 stop。"
		parameters, e := info.ParamsOneOf.ToJSONSchema()
		if e != nil {
			return nil, e
		}
		if action, ok := parameters.Properties.Get("action"); ok {
			action.Enum = []any{"start", "get_app_state", "type_text", "click_element", "stop"}
		}
		if app, ok := parameters.Properties.Get("appId"); ok {
			app.Enum = []any{fixtureID}
		}
		info.ParamsOneOf = schema.NewParamsOneOfByJSONSchema(parameters)
	}
	return info, err
}
func (t *auditedTool) verifyState(state *computeruse.ToolResult) {
	// 以最后一次真实观察为准，不能用曾经出现过的正确值掩盖后续错误。
	t.report.Checks["visual_code_entered_and_read_back"] = false
	t.report.Checks["button_effect_read_back"] = false
	encoded, _ := json.Marshal(state.Details["elements"])
	var elements []computeruse.Element
	_ = json.Unmarshal(encoded, &elements)
	for _, e := range elements {
		if e.Label == "Eino 验证输入框" && fmt.Sprint(e.Value) == "模型验收："+t.code {
			t.report.Checks["visual_code_entered_and_read_back"] = true
		}
		if strings.Contains(e.Label, "Eino 验证按钮，已点击 1 次") {
			t.report.Checks["button_effect_read_back"] = true
		}
	}
}
func (t *auditedTool) InvokableRun(ctx context.Context, args *schema.ToolArgument, options ...tool.Option) (*schema.ToolResult, error) {
	var p computeruse.Param
	if args == nil {
		return nil, fmt.Errorf("缺少工具参数")
	}
	if err := json.Unmarshal([]byte(args.Text), &p); err != nil {
		return nil, err
	}
	if p.Action == computeruse.ActionListApps || (p.Action == computeruse.ActionStart && p.AppID != fixtureID) {
		t.report.Actions = append(t.report.Actions, actionRecord{Action: p.Action, Arguments: args.Text, Error: "目标超出验证范围"})
		return nil, fmt.Errorf("验证只允许直接操作指定的独立测试应用")
	}
	if p.Action == computeruse.ActionStop {
		state, err := t.Tool.ExecuteContext(ctx, &computeruse.Param{Action: computeruse.ActionGetAppState})
		if err != nil {
			return nil, err
		}
		if state.Error != "" {
			return nil, fmt.Errorf("停止前无法确认最终状态：%s", state.Error)
		}
		t.verifyState(state)
	}
	result, err := t.Tool.InvokableRun(ctx, args, options...)
	record := actionRecord{Action: p.Action, Arguments: args.Text, Passed: err == nil}
	observation := t.report.Images + 1
	if err != nil {
		record.Error = err.Error()
	} else {
		for _, part := range result.Parts {
			if part.Image != nil {
				t.report.Images++
				if part.Image.Base64Data != nil {
					data, e := base64.StdEncoding.DecodeString(*part.Image.Base64Data)
					if e != nil {
						return nil, e
					}
					suffix := ".jpeg"
					if part.Image.MIMEType == "image/png" {
						suffix = ".png"
					}
					if e = os.WriteFile(filepath.Join(t.directory, fmt.Sprintf("model-observation-%d%s", observation, suffix)), data, 0600); e != nil {
						return nil, e
					}
				}
			}
			if part.Type != schema.ToolPartTypeText {
				continue
			}
			var state computeruse.ToolResult
			if err := json.Unmarshal([]byte(part.Text), &state); err != nil {
				return nil, err
			}
			if state.Error != "" {
				record.Passed = false
				record.Error = state.Error
			}
			if p.Action == computeruse.ActionGetAppState && state.Error == "" {
				if e := os.WriteFile(filepath.Join(t.directory, fmt.Sprintf("model-observation-%d.json", observation)), []byte(part.Text), 0600); e != nil {
					return nil, e
				}
				if !t.observed {
					t.report.Checks["visual_code_absent_from_ax"] = !strings.Contains(part.Text, t.code)
					t.observed = true
				}
				t.verifyState(&state)
			}
		}
	}
	if p.Action == computeruse.ActionStop && record.Passed {
		t.report.Checks["model_stopped_session"] = true
	}
	t.report.Actions = append(t.report.Actions, record)
	fmt.Fprintf(os.Stderr, "模型工具调用：%s，成功=%t\n", p.Action, record.Passed)
	return result, err
}

type countedModel struct {
	inner  model.ToolCallingChatModel
	report *modelReport
}

func (m *countedModel) WithTools(info []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	bound, err := m.inner.WithTools(info)
	return &countedModel{inner: bound, report: m.report}, err
}
func (m *countedModel) Generate(ctx context.Context, messages []*schema.Message, options ...model.Option) (*schema.Message, error) {
	m.report.ModelCalls++
	output, err := m.inner.Generate(ctx, messages, options...)
	if output != nil && output.ResponseMeta != nil && output.ResponseMeta.Usage != nil {
		m.report.Tokens += output.ResponseMeta.Usage.TotalTokens
	}
	return output, err
}
func (m *countedModel) Stream(ctx context.Context, messages []*schema.Message, options ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return m.inner.Stream(ctx, messages, options...)
}
func redact(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		text = strings.ReplaceAll(text, key, "[REDACTED]")
	}
	return text
}
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, redact(err))
		os.Exit(1)
	}
}
func run() (runErr error) {
	helper := flag.String("helper", "", "原生 helper 路径")
	path := flag.String("report", "model-verification.json", "报告路径")
	requestPermissions := flag.Bool("request-permissions", false, "显式请求系统权限")
	flag.Parse()
	r := &modelReport{StartedAt: time.Now(), Model: os.Getenv("OPENAI_MODEL_NAME"), Checks: map[string]bool{}}
	defer func() {
		if runErr != nil {
			r.Passed = false
			r.Reason = redact(runErr)
		}
		data, _ := json.MarshalIndent(r, "", "  ")
		_ = os.WriteFile(*path, data, 0600)
		fmt.Println(string(data))
	}()
	for _, key := range []string{"OPENAI_API_KEY", "OPENAI_MODEL_NAME", "OPENAI_BASE_URL", "EINO_VALIDATION_VISUAL_CODE"} {
		if os.Getenv(key) == "" {
			return fmt.Errorf("环境变量 %s 未配置", key)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	maxTokens := 2048
	cm, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{APIKey: os.Getenv("OPENAI_API_KEY"), BaseURL: os.Getenv("OPENAI_BASE_URL"), Model: r.Model, MaxTokens: &maxTokens, Timeout: 45 * time.Second})
	if err != nil {
		return err
	}
	settings := computeruse.DefaultSettings()
	settings.Enabled = true
	desktop, err := computeruse.NewComputerUseTool(ctx, &computeruse.Config{
		Settings: &settings, Providers: []computeruse.Provider{computeruse.NewMacOSProvider(&computeruse.MacOSConfig{Command: *helper, DisableCursor: true, SocketPath: filepath.Join(filepath.Dir(*path), "model-helper.sock")})},
		DefaultProviderID: "macos:cua", Model: &computeruse.ModelInfo{Input: []string{"image"}},
		ConfirmApp: func(_ context.Context, request computeruse.ApprovalRequest) (computeruse.ApprovalDecision, error) {
			status := "denied"
			if request.Approval.AppID == fixtureID {
				status = "approved"
			}
			return computeruse.ApprovalDecision{Status: status, Scope: "session"}, nil
		},
	})
	if err != nil {
		return err
	}
	defer func() {
		if err := desktop.Cleanup(); runErr == nil && err != nil {
			runErr = err
		}
	}()
	if *requestPermissions {
		if _, err = desktop.Host().RequestPermissions(ctx, "macos:cua"); err != nil {
			return err
		}
	}
	audited := &auditedTool{Tool: desktop, report: r, code: os.Getenv("EINO_VALIDATION_VISUAL_CODE"), directory: filepath.Dir(*path)}
	a, err := react.NewAgent(ctx, &react.AgentConfig{
		ToolCallingModel: &countedModel{inner: cm, report: r}, MessageModifier: computeruse.ToolImagesAsUserMessages,
		ToolsConfig: compose.ToolsNodeConfig{Tools: []tool.BaseTool{audited}, ExecuteSequentially: true}, MaxStep: 24,
	})
	if err != nil {
		return err
	}
	output, err := a.Generate(ctx, []*schema.Message{
		schema.SystemMessage("你正在执行一次独立窗口验收。只允许操作 local.eino.computeruse.validation。该应用已运行，直接 start，不要枚举其他应用。只使用最新 get_app_state 返回的元素编号，每个动作后重新观察。不要操作键盘回退框、滚动区或次级动作按钮。完成后 stop。"),
		schema.UserMessage("观察验证窗口截图，读取黄色区域绘制的六位视觉验证码。该验证码不在无障碍文本中。将‘模型验收：’加上该六位数字输入到‘Eino 验证输入框’，点击普通的‘Eino 验证按钮’一次。再次观察，确认输入内容和按钮已点击 1 次，然后停止控制并报告完成。"),
	})
	if err != nil {
		return err
	}
	r.Final = output.Content
	r.Checks["real_model_called"] = r.ModelCalls > 0
	r.Checks["screenshots_returned"] = r.Images > 0
	r.Checks["all_tool_calls_succeeded"] = true
	for _, action := range r.Actions {
		if !action.Passed {
			r.Checks["all_tool_calls_succeeded"] = false
		}
	}
	for _, name := range []string{"real_model_called", "screenshots_returned", "visual_code_absent_from_ax", "visual_code_entered_and_read_back", "button_effect_read_back", "model_stopped_session"} {
		if !r.Checks[name] {
			return fmt.Errorf("真实模型验收断言失败：%s", name)
		}
	}
	r.Passed = true
	return nil
}
