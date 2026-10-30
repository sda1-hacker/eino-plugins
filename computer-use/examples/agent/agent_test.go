package agent

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	computeruse "github.com/sda1-hacker/eino-plugins/computer-use"
)

// 脚本模型只替代模型服务；Agent、ToolsNode、工具和多模态消息都使用真实实现。
type scriptedModel struct {
	t        *testing.T
	step     int
	sawImage bool
	sawText  bool
}

func (m *scriptedModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	if len(tools) != 1 || tools[0].Name != "computer" {
		return nil, fmt.Errorf("没有绑定 computer 工具")
	}
	return m, nil
}
func (m *scriptedModel) Generate(ctx context.Context, messages []*schema.Message, _ ...model.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, msg := range messages {
		for _, part := range msg.UserInputMultiContent {
			if part.Image != nil && part.Image.Base64Data != nil {
				m.sawImage = true
			}
			if strings.Contains(part.Text, "ReAct 中文验证") {
				m.sawText = true
			}
		}
	}
	steps := []string{`{"action":"start","appId":"app.notes"}`, `{"action":"get_app_state"}`, `{"action":"type_text","elementId":"mock-input","text":"ReAct 中文验证"}`, `{"action":"get_app_state"}`, `{"action":"stop"}`}
	if m.step >= len(steps) {
		return schema.AssistantMessage("任务完成", nil), nil
	}
	args := steps[m.step]
	m.step++
	return schema.AssistantMessage("", []schema.ToolCall{{ID: fmt.Sprint(m.step), Type: "function", Function: schema.FunctionCall{Name: "computer", Arguments: args}}}), nil
}
func (m *scriptedModel) Stream(ctx context.Context, messages []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	out, err := m.Generate(ctx, messages, opts...)
	if err != nil {
		return nil, err
	}
	return schema.StreamReaderFromArray([]*schema.Message{out}), nil
}
func TestReActReceivesScreenshotsAndVerifiesAction(t *testing.T) {
	settings := computeruse.DefaultSettings()
	settings.Enabled = true
	cm := &scriptedModel{t: t}
	a, cleanup, err := New(context.Background(), cm, &computeruse.Config{Settings: &settings, Providers: []computeruse.Provider{computeruse.NewMockProvider()}, DefaultProviderID: "mock", Model: &computeruse.ModelInfo{Input: []string{"image"}}})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	out, err := a.Generate(context.Background(), []*schema.Message{schema.UserMessage("输入中文并检查结果")})
	if err != nil {
		t.Fatal(err)
	}
	if out.Content != "任务完成" || !cm.sawImage || !cm.sawText {
		t.Fatalf("ReAct loop did not preserve observations: output=%s image=%v text=%v", out.Content, cm.sawImage, cm.sawText)
	}
}
