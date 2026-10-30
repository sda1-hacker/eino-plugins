package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/middlewares/filesystem"
	"github.com/sda1-hacker/eino-plugins/sandboxfs"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {

	prompt := "执行一下 /Users/sda1_hacker/Desktop/OH-WorkSpace/OH-Works 的 create.sh 这个脚本"
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	apiKey := os.Getenv("OPENAI_API_KEY")
	baseUrl := os.Getenv("OPENAI_BASE_URL")
	modelName := os.Getenv("OPENAI_MODEL_NAME")

	model, err := openai.NewChatModel(ctx, &openai.ChatModelConfig{
		APIKey:  apiKey,
		BaseURL: baseUrl,
		Model:   modelName,
	})
	if err != nil {
		return fmt.Errorf("模型创建失败: %w", err)
	}

	workspace := "/Users/sda1_hacker/Desktop/OH-WorkSpace/OH-Works"
	backend, err := sandboxfs.NewBackend(ctx, &sandboxfs.Config{
		Workspace:    workspace,
		ShellTimeout: time.Minute,
	})
	if err != nil {
		return fmt.Errorf("Backend 创建失败: %w", err)
	}
	defer backend.Close()

	bdMiddleware, err := filesystem.New(ctx, &filesystem.MiddlewareConfig{
		Backend:        backend,
		StreamingShell: backend,
	})
	if err != nil {
		return fmt.Errorf("Middleware 创建失败: %w", err)
	}

	agent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Instruction: "你是一个个人 Ai 助手，全程使用中文进行回复",
		// Instruction: fmt.Sprintf("你的工作区是 %s。文件工具使用该目录内的实际绝对路径。Shell 从该工作区执行，可使用实际绝对路径或工作区相对路径。", workspace),
		Name:     "个人Agent助手",
		Handlers: []adk.ChatModelAgentMiddleware{bdMiddleware},
		Model:    model,
	})
	if err != nil {
		return fmt.Errorf("Agent 创建失败: %w", err)
	}

	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           agent,
		EnableStreaming: true,
		CheckPointStore: nil,
	})

	iter := runner.Query(ctx, prompt)
	i := 1
	for {
		fmt.Printf("%d ================================== \n", i)
		i++
		event, ok := iter.Next()
		if !ok {
			break
		}
		if event.Err != nil {
			return fmt.Errorf("Agent 运行失败: %w", event.Err)
		}
		if event.Output == nil || event.Output.MessageOutput == nil {
			continue
		}
		message, err := event.Output.MessageOutput.GetMessage()
		if err != nil {
			return fmt.Errorf("读取 Agent 输出失败: %w", err)
		}
		//if message == nil {
		//	continue
		//}
		//if message.ReasoningContent != "" {
		//	fmt.Printf("ReasoningContent: [%s] %s \n", message.Role, message.ReasoningContent)
		//}
		//if message.Content != "" {
		//	fmt.Printf("Content: [%s] %s\n", message.Role, message.Content)
		//}
		//for _, call := range message.ToolCalls {
		//	fmt.Printf("ToolCalls: %s %s\n", call.Function.Name, call.Function.Arguments)
		//}
		fmt.Printf("[res: ] \n %v \n", message)
	}
	return nil
}
