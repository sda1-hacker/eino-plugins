package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/sda1-hacker/eino-plugins/sandboxfs"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	workspace := flag.String("workspace", "/Users/sda1_hacker/Desktop/OH-WorkSpace/OH-Works", "沙箱工作区目录")
	command := flag.String("command", "bash ./create.sh", "在沙箱工作区内执行的命令")
	allowNetwork := flag.Bool("allow-network", false, "允许沙箱命令联网（默认禁网）")
	flag.Parse()

	backend, err := sandboxfs.NewBackend(context.Background(), &sandboxfs.Config{
		Workspace:    *workspace,
		ShellTimeout: time.Minute,
		AllowNetwork: *allowNetwork,
	})
	if err != nil {
		return fmt.Errorf("Backend 创建失败: %w", err)
	}
	defer backend.Close()
	result, err := backend.Execute(context.Background(), &filesystem.ExecuteRequest{
		Command: *command,
	})
	if err != nil {
		return fmt.Errorf("Shell 执行失败: %w", err)
	}

	fmt.Printf("执行输出:\n%s\n输出截断: %t\n", result.Output, result.Truncated)

	if result.ExitCode == nil {
		return fmt.Errorf("Shell 未返回退出码")
	}
	fmt.Printf("退出码: %d\n", *result.ExitCode)

	if *result.ExitCode != 0 {
		return fmt.Errorf("命令执行失败（退出码 %d），请检查上述输出", *result.ExitCode)
	}
	return nil
}
