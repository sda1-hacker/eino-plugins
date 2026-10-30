// desktop 连接真实桌面；必须显式传入目标应用，默认只读取状态。
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"time"

	computeruse "github.com/sda1-hacker/eino-plugins/computer-use"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	appID := flag.String("app", "", "要批准并读取的应用标识，例如 com.apple.TextEdit 或 pid:123")
	provider := flag.String("provider", "", "Provider ID；默认根据当前系统选择")
	helper := flag.String("helper", "", "macOS helper 可执行文件路径")
	permissions := flag.Bool("request-permissions", false, "请求 macOS 无障碍和屏幕录制权限")
	flag.Parse()
	if *appID == "" {
		flag.Usage()
		return fmt.Errorf("需要通过 -app 指定应用")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	ctx, timeout := context.WithTimeout(ctx, time.Minute)
	defer timeout()
	settings := computeruse.DefaultSettings()
	settings.Enabled = true
	desktop, err := computeruse.NewComputerUseTool(ctx, &computeruse.Config{Settings: &settings, DefaultProviderID: *provider, Model: &computeruse.ModelInfo{Input: []string{"image"}}, MacOS: &computeruse.MacOSConfig{Command: *helper}, ConfirmApp: func(_ context.Context, request computeruse.ApprovalRequest) (computeruse.ApprovalDecision, error) {
		if request.Approval.AppID != *appID {
			return computeruse.ApprovalDecision{Status: "denied"}, nil
		}
		return computeruse.ApprovalDecision{Status: "approved", Scope: "session"}, nil
	}})
	if err != nil {
		return err
	}
	defer func() {
		if err := desktop.Cleanup(); err != nil {
			log.Print(err)
		}
	}()
	if *permissions {
		status, err := desktop.Host().RequestPermissions(ctx, *provider)
		if err != nil {
			return err
		}
		fmt.Printf("系统权限：%+v\n", status)
	}
	for _, p := range []*computeruse.Param{
		{Action: computeruse.ActionStart, AppID: *appID},
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
			fmt.Printf("已读取窗口截图：%s（%d 个 Base64 字符）\n", result.MIMEType, len(result.Base64Image))
		}
	}
	return nil
}
