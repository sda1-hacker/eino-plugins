package computeruse

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// 使用测试二进制充当子进程，可跨平台验证 runner，不依赖 shell 或真实 GUI。
func TestRunnerChild(t *testing.T) {
	if os.Getenv("EINO_COMPUTER_USE_TEST_CHILD") != "1" {
		return
	}
	switch os.Args[len(os.Args)-1] {
	case "echo":
		fmt.Print("原生 JSON 输出")
		fmt.Fprint(os.Stderr, "diagnostic")
		os.Exit(7)
	case "large":
		fmt.Print(strings.Repeat("x", 4096))
		os.Exit(0)
	case "wait":
		time.Sleep(time.Minute)
		os.Exit(0)
	}
}
func childCommand(mode string) Command {
	return Command{Name: os.Args[0], Args: []string{"-test.run=^TestRunnerChild$", "--", mode}, Env: map[string]string{"EINO_COMPUTER_USE_TEST_CHILD": "1"}}
}
func TestExecRunnerExitTimeoutAndBounds(t *testing.T) {
	runner := ExecRunner{}
	r, err := runner.Run(context.Background(), childCommand("echo"))
	// 依赖包可能在 init 时输出运行时兼容提示；runner 应原样保留这些诊断。
	if err != nil || r.ExitCode != 7 || r.Stdout != "原生 JSON 输出" || !strings.HasSuffix(r.Stderr, "diagnostic") {
		t.Fatalf("exit result: %+v %v", r, err)
	}
	c := childCommand("large")
	c.MaxOutputBytes = 10
	r, err = runner.Run(context.Background(), c)
	if err == nil || len(r.Stdout) != 10 {
		t.Fatalf("output bound: %d %v", len(r.Stdout), err)
	}
	c = childCommand("wait")
	c.Timeout = 50 * time.Millisecond
	start := time.Now()
	_, err = runner.Run(context.Background(), c)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 3*time.Second {
		t.Fatalf("timeout failed: %v", err)
	}
}
func TestExecRunnerDaemonIsReaped(t *testing.T) {
	p, err := (ExecRunner{}).Start(childCommand("wait"))
	if err != nil {
		t.Fatal(err)
	}
	if err = p.Kill(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("killed process not reaped")
	}
}
