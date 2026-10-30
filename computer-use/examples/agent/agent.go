// Package agent 演示将 Computer Use 挂入已有 Eino ChatModel，避免绑定具体模型厂商。
package agent

import (
	"context"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	computeruse "github.com/sda1-hacker/eino-plugins/computer-use"
)

// New 返回 ReAct Agent 与清理函数；应用应在 Generate/Stream 结束后按生命周期清理。
func New(ctx context.Context, chatModel model.ToolCallingChatModel, config *computeruse.Config) (*react.Agent, func() error, error) {
	desktop, err := computeruse.NewComputerUseTool(ctx, config)
	if err != nil {
		return nil, nil, err
	}
	agent, err := react.NewAgent(ctx, &react.AgentConfig{
		ToolCallingModel: chatModel,
		MessageModifier:  computeruse.ToolImagesAsUserMessages,
		ToolsConfig:      compose.ToolsNodeConfig{Tools: []tool.BaseTool{desktop}, ExecuteSequentially: true},
		MaxStep:          40,
	})
	if err != nil {
		_ = desktop.Cleanup()
		return nil, nil, err
	}
	return agent, desktop.Cleanup, nil
}
