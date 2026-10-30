package computeruse

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// CommandRunner 可注入测试替身；参数独立传递，禁止经 shell 拼接执行。
type Command struct {
	Name           string
	Args           []string
	Env            map[string]string
	Timeout        time.Duration
	MaxOutputBytes int
}
type CommandResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}
type ManagedProcess interface {
	Kill() error
	Done() <-chan struct{}
}
type CommandRunner interface {
	Run(context.Context, Command) (CommandResult, error)
	Start(Command) (ManagedProcess, error)
}
type ExecRunner struct{}

type boundedBuffer struct {
	// 不嵌入 bytes.Buffer，避免 io.Copy 调用其 ReaderFrom 绕过下面的容量检查。
	buffer   bytes.Buffer
	limit    int
	overflow bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.buffer.Len()
	if remaining < 0 {
		remaining = 0
	}
	if len(p) > remaining {
		p = p[:remaining]
		b.overflow = true
	}
	_, _ = b.buffer.Write(p)
	return n, nil
}

func (b *boundedBuffer) String() string { return b.buffer.String() }
func makeCommand(c Command) *exec.Cmd {
	cmd := exec.Command(c.Name, c.Args...)
	env := map[string]string{}
	for _, entry := range os.Environ() {
		k, v, ok := strings.Cut(entry, "=")
		if ok {
			env[k] = v
		}
	}
	for k, v := range c.Env {
		env[k] = v
	}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	configureProcess(cmd)
	return cmd
}
func (ExecRunner) Run(ctx context.Context, c Command) (CommandResult, error) {
	if c.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, c.Timeout)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		return CommandResult{}, err
	}
	cmd := makeCommand(c)
	limit := c.MaxOutputBytes
	if limit <= 0 {
		limit = 32 << 20
	}
	out := &boundedBuffer{limit: limit}
	stderr := &boundedBuffer{limit: 64 << 10}
	cmd.Stdout = out
	cmd.Stderr = stderr
	cmd.WaitDelay = time.Second
	if err := cmd.Start(); err != nil {
		return CommandResult{}, err
	}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = killProcess(cmd)
		case <-done:
		}
	}()
	err := cmd.Wait()
	close(done)
	result := CommandResult{Stdout: out.String(), Stderr: stderr.String(), ExitCode: cmd.ProcessState.ExitCode()}
	if ctx.Err() != nil {
		return result, ctx.Err()
	}
	if out.overflow || stderr.overflow {
		return result, fmt.Errorf("helper 输出超过大小限制")
	}
	var exitErr *exec.ExitError
	if err != nil && !asExitError(err, &exitErr) {
		return result, err
	}
	return result, nil
}
func asExitError(err error, target **exec.ExitError) bool {
	v, ok := err.(*exec.ExitError)
	if ok {
		*target = v
	}
	return ok
}

type execProcess struct {
	cmd  *exec.Cmd
	done chan struct{}
	once sync.Once
}

func (p *execProcess) Kill() error {
	var err error
	p.once.Do(func() { err = killProcess(p.cmd) })
	return err
}
func (p *execProcess) Done() <-chan struct{} { return p.done }
func (ExecRunner) Start(c Command) (ManagedProcess, error) {
	cmd := makeCommand(c)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	p := &execProcess{cmd: cmd, done: make(chan struct{})}
	go func() { _ = cmd.Wait(); close(p.done) }()
	return p, nil
}
