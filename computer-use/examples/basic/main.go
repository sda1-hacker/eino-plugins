// basic 演示不依赖真实桌面或模型 API 的完整控制闭环。
package main

import (
	"context"
	"fmt"
	"log"

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
		Model:             &computeruse.ModelInfo{Input: []string{"text", "image"}},
	})
	if err != nil {
		return err
	}
	defer desktop.Cleanup()
	text := "你好，Eino！"
	for _, p := range []*computeruse.Param{
		{Action: computeruse.ActionStart, AppID: "app.notes"},
		{Action: computeruse.ActionGetAppState},
		{Action: computeruse.ActionTypeText, ElementID: "mock-input", Text: &text},
		{Action: computeruse.ActionGetAppState},
		{Action: computeruse.ActionStop},
	} {
		result, err := desktop.ExecuteContext(ctx, p)
		if err != nil {
			return err
		}
		if result.Error != "" {
			return fmt.Errorf("%s: %s", result.ErrorCode, result.Error)
		}
		fmt.Println(result.Output)
		if result.Base64Image != "" {
			fmt.Printf("截图：%s，Base64 长度 %d\n", result.MIMEType, len(result.Base64Image))
		}
	}
	return nil
}
