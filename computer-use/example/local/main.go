// local 使用模拟桌面演示 Eino ToolsNode 调用，不需要模型 API 或系统权限。
package main

import (
	"context"
	"fmt"
	"log"
	"strconv"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	computeruse "github.com/sda1-hacker/eino-plugins/computer-use"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx := context.Background()
	settings := computeruse.DefaultSettings()
	settings.Enabled = true
	desktop, err := computeruse.NewComputerUseTool(ctx, &computeruse.Config{
		Settings:          &settings,
		Providers:         []computeruse.Provider{computeruse.NewMockProvider()},
		DefaultProviderID: "mock",
		Model:             &computeruse.ModelInfo{Input: []string{"image"}},
	})
	if err != nil {
		return err
	}
	defer desktop.Cleanup()
	node, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{Tools: []tool.BaseTool{desktop}, ExecuteSequentially: true})
	if err != nil {
		return err
	}
	// 实际 Agent 中，这些 ToolCalls 来自支持工具调用和图片输入的 ChatModel。
	for i, args := range []string{
		`{"action":"start","appId":"app.notes"}`,
		`{"action":"get_app_state"}`,
		`{"action":"type_text","elementId":"mock-input","text":"Eino 多模态工具"}`,
		`{"action":"get_app_state"}`,
		`{"action":"stop"}`,
	} {
		messages, err := node.Invoke(ctx, schema.AssistantMessage("", []schema.ToolCall{{ID: strconv.Itoa(i), Type: "function", Function: schema.FunctionCall{Name: "computer", Arguments: args}}}))
		if err != nil {
			return err
		}
		for _, m := range messages {
			fmt.Println(m.Content)
			for _, part := range m.UserInputMultiContent {
				if part.Text != "" {
					fmt.Println(part.Text)
				}
				if part.Image != nil {
					fmt.Printf("Eino 已接收到图片 part：%s\n", part.Image.MIMEType)
				}
			}
		}
	}
	return nil
}
