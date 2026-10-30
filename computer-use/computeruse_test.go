package computeruse

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
)

func testTool(t *testing.T) *Tool {
	t.Helper()
	tool, err := NewComputerUseTool(context.Background(), testConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tool.Cleanup(); err != nil {
			t.Error(err)
		}
	})
	return tool
}
func TestToolEinoMultimodalRoundTrip(t *testing.T) {
	ctx := context.Background()
	desktop := testTool(t)
	node, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{Tools: []tool.BaseTool{desktop}, ExecuteSequentially: true})
	if err != nil {
		t.Fatal(err)
	}
	call := func(id, args string) *schema.Message {
		t.Helper()
		out, err := node.Invoke(ctx, schema.AssistantMessage("", []schema.ToolCall{{ID: id, Type: "function", Function: schema.FunctionCall{Name: "computer", Arguments: args}}}))
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 1 || out[0].ToolCallID != id {
			t.Fatalf("unexpected tool messages: %+v", out)
		}
		return out[0]
	}
	call("start", `{"action":"start","appId":"app.notes"}`)
	state := call("state", `{"action":"get_app_state"}`)
	if len(state.UserInputMultiContent) != 2 {
		t.Fatalf("image must survive ToolsNode: %+v", state)
	}
	parts := state.UserInputMultiContent
	if parts[1].Image == nil || parts[1].Image.Base64Data == nil || parts[1].Image.MIMEType != "image/png" {
		t.Fatal("missing native image part")
	}
	if strings.Contains(parts[0].Text, "base64_image") {
		t.Fatal("image duplicated into text")
	}
	var result ToolResult
	if err = json.Unmarshal([]byte(parts[0].Text), &result); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.Output, "mock-input") {
		t.Fatal("missing element summary")
	}
	typed := call("type", `{"action":"type_text","elementId":"mock-input","text":"你好 Eino"}`)
	if strings.Contains(typed.Content, `"error"`) {
		t.Fatal(typed.Content)
	}
	next := call("verify", `{"action":"get_app_state"}`)
	if !strings.Contains(next.UserInputMultiContent[0].Text, "你好 Eino") {
		t.Fatal("action effect not visible")
	}
	call("stop", `{"action":"stop"}`)
}
func TestToolRejectsMalformedAndHiddenActions(t *testing.T) {
	desktop := testTool(t)
	for _, args := range []string{`{`, `null`, `{"action":"start"}`, `{"action":"click_point","x":1}`, `{"action":"status","owner":"forged"}`, `{"action":"status"} {}`, `{"action":"type_text"}`, `{"action":"scroll","direction":"diagonal"}`, `{"action":"scroll","direction":"down","amount":-2}`} {
		t.Run(args, func(t *testing.T) {
			r, err := desktop.InvokableRun(context.Background(), &schema.ToolArgument{Text: args})
			if err != nil {
				t.Fatal(err)
			}
			var result ToolResult
			if err = json.Unmarshal([]byte(r.Parts[0].Text), &result); err != nil {
				t.Fatal(err)
			}
			if result.ErrorCode == "" {
				t.Fatalf("accepted invalid arguments: %s", args)
			}
		})
	}
	info, _ := desktop.Info(context.Background())
	js, _ := info.ParamsOneOf.ToJSONSchema()
	b, _ := json.Marshal(js)
	for _, a := range []string{"click_point", "double_click", "drag", "snapshotElement"} {
		if strings.Contains(string(b), a) {
			t.Fatalf("hidden field exposed: %s", a)
		}
	}
}
func TestTextAdapterAndCleanup(t *testing.T) {
	desktop := testTool(t)
	out, err := desktop.AsTextTool().InvokableRun(context.Background(), `{"action":"status"}`)
	if err != nil || !strings.Contains(out, "enabled") {
		t.Fatalf("text adapter: %s %v", out, err)
	}
	if err = desktop.Cleanup(); err != nil {
		t.Fatal(err)
	}
	if err = desktop.Cleanup(); err != nil {
		t.Fatal(err)
	}
	r, err := desktop.Execute(&Param{Action: ActionStatus})
	if err != nil || r.ErrorCode != ErrDisabled {
		t.Fatalf("closed tool remains usable: %+v %v", r, err)
	}
}
func TestSharedHostCleanupDoesNotCloseOtherTools(t *testing.T) {
	h := testHost(t, testConfig())
	a, err := NewComputerUseTool(context.Background(), &Config{Host: h})
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewComputerUseTool(context.Background(), &Config{Host: h})
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Cleanup(); err != nil {
		t.Fatal(err)
	}
	r, err := b.Execute(&Param{Action: ActionStart, AppID: "app.notes"})
	if err != nil || r.Error != "" {
		t.Fatalf("shared host closed unexpectedly: %+v %v", r, err)
	}
}
