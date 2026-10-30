//go:build darwin || linux || windows

// Package sandboxfs provides an Eino filesystem backend with workspace path
// guards and sandboxed command execution on macOS, Linux and Windows.
// Windows execution is experimental pending validation on Windows.
package sandboxfs

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/cloudwego/eino/schema"
)

const (
	maxFileBytes   = 4 << 20 // 单文件最多 4 MiB
	maxShellOutput = 1 << 20 // 命令输出最多 1 MiB
	maxWalkEntries = 10000
	maxMatches     = 1000
)

var (
	// ErrDenied 表示路径越出工作区或操作超出后端支持范围。
	ErrDenied = errors.New("sandboxfs: 路径或操作被拒绝")
	// ErrShellUnavailable 表示当前平台或系统组件不能提供安全 Shell。
	ErrShellUnavailable = errors.New("sandboxfs: 安全 Shell 不可用")
)

// Windows 需要原生 STARTUPINFOEX；macOS/Linux 仍直接使用 os/exec。
type sandboxCommand interface {
	Start(*os.File) error
	Wait() error
	Cancel() error
}

type execCommand struct{ *exec.Cmd }

func (c execCommand) Start(output *os.File) error {
	c.Stdout, c.Stderr = output, output
	return c.Cmd.Start()
}

func (c execCommand) Cancel() error { return c.Cmd.Cancel() }

// Config 配置文件操作和命令执行共用的工作区。
type Config struct {
	// ValidateCommand 与 Eino local 一致；可选，在进入系统沙箱前检查命令。
	ValidateCommand func(string) error
	// Workspace 必填；文件工具和 Shell 使用该目录的绝对物理路径。
	Workspace string
	// ShellTimeout 默认为 60 秒，最长 5 分钟。
	ShellTimeout time.Duration
	// AllowNetwork 允许 Shell 及其子进程联网；默认 false，不影响文件路径守卫。
	AllowNetwork bool
}

// Backend 同时实现 Eino 的文件操作、同步 Shell 和流式 Shell 接口。
// 每个 Agent 会话使用独立 Backend；使用完成后调用 Close。
type Backend struct {
	validateCommand func(string) error
	root            *os.Root
	workspace       string
	shellTimeout    time.Duration
	allowNetwork    bool
	mu              sync.Mutex // 本 Backend 的 write/edit 串行化，Shell 的修改不在此锁内
}

var (
	_ filesystem.Backend        = (*Backend)(nil)
	_ filesystem.Shell          = (*Backend)(nil)
	_ filesystem.StreamingShell = (*Backend)(nil)
)

// NewBackend 与 Eino Local 使用相同的 context + Config 构造形式。
// 文件工具不依赖系统沙箱组件；Execute/ExecuteStreaming 在执行前检查组件。
func NewBackend(_ context.Context, cfg *Config) (*Backend, error) {
	if cfg == nil {
		return nil, errors.New("sandboxfs: config 不能为空")
	}
	if cfg.Workspace == "" {
		return nil, ErrDenied
	}
	timeout := cfg.ShellTimeout
	if timeout <= 0 {
		timeout = time.Minute
	}
	if timeout > 5*time.Minute {
		return nil, fmt.Errorf("沙箱超时不得超过 5 分钟")
	}
	workspace, err := filepath.Abs(cfg.Workspace)
	if err != nil {
		return nil, err
	}
	canonical, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(workspace)
	if err != nil {
		return nil, err
	}
	return &Backend{root: r, workspace: canonical, shellTimeout: timeout,
		validateCommand: cfg.ValidateCommand, allowNetwork: cfg.AllowNetwork}, nil
}

// Close 释放工作区目录句柄。调用方应先结束操作，并取消仍在执行的命令的 ctx。
func (b *Backend) Close() error { return b.root.Close() }

func (b *Backend) LsInfo(ctx context.Context, req *filesystem.LsInfoRequest) ([]filesystem.FileInfo, error) {
	if req == nil {
		return nil, ErrDenied
	}
	rel, err := b.resolve(req.Path)
	if err != nil {
		return nil, err
	}
	dir, err := openRootRead(b.root, rel)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(1025)
	if err != nil && err != io.EOF {
		return nil, err
	}
	if len(entries) > 1024 {
		return nil, fmt.Errorf("目录条目超限")
	}
	out := make([]filesystem.FileInfo, 0, len(entries))
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// 不通过链接递归访问外部资源；避免误导模型。
		if e.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		out = append(out, filesystem.FileInfo{
			Path: filepath.Join(b.workspace, rel, e.Name()), IsDir: info.IsDir(),
			Size: info.Size(), ModifiedAt: info.ModTime().UTC().Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	return out, nil
}

func (b *Backend) Read(ctx context.Context, req *filesystem.ReadRequest) (*filesystem.FileContent, error) {
	if req == nil {
		return nil, ErrDenied
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rel, err := b.resolve(req.FilePath)
	if err != nil {
		return nil, err
	}
	data, err := b.readFile(rel)
	if err != nil {
		return nil, err
	}
	offset, limit := req.Offset, req.Limit
	if offset < 1 {
		offset = 1
	}
	if limit <= 0 {
		limit = 2000
	}
	if limit > 2000 {
		limit = 2000
	}
	var sb strings.Builder
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), maxFileBytes+1)
	linesWritten := 0
	for line := 1; sc.Scan(); line++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if line < offset {
			continue
		}
		if line >= offset+limit {
			break
		}
		if linesWritten > 0 {
			sb.WriteByte('\n')
		}
		sb.Write(sc.Bytes())
		linesWritten++
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return &filesystem.FileContent{Content: sb.String()}, nil
}

func (b *Backend) Write(ctx context.Context, req *filesystem.WriteRequest) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if req == nil {
		return ErrDenied
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	rel, err := b.resolve(req.FilePath)
	if err != nil {
		return err
	}
	return b.put(rel, []byte(req.Content))
}

func (b *Backend) Edit(ctx context.Context, req *filesystem.EditRequest) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if req == nil || req.OldString == "" || req.OldString == req.NewString {
		return ErrDenied
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	rel, err := b.resolve(req.FilePath)
	if err != nil {
		return err
	}
	buf, err := b.readFile(rel)
	if err != nil {
		return err
	}
	content := string(buf)
	count := strings.Count(content, req.OldString)
	if count == 0 {
		return fmt.Errorf("待替换文本不存在")
	}
	if count != 1 && !req.ReplaceAll {
		return fmt.Errorf("待替换文本出现多次")
	}
	n := 1
	if req.ReplaceAll {
		n = -1
	}
	return b.put(rel, []byte(strings.Replace(content, req.OldString, req.NewString, n)))
}

func (b *Backend) GrepRaw(ctx context.Context, req *filesystem.GrepRequest) ([]filesystem.GrepMatch, error) {
	if req == nil || req.Pattern == "" {
		return nil, ErrDenied
	}
	// 第一版不假装完整模拟 rg 的多行与上下文语义。
	if req.EnableMultiline || req.BeforeLines != 0 || req.AfterLines != 0 {
		return nil, fmt.Errorf("grep 暂不支持多行模式或上下文行")
	}
	rel, err := b.resolve(req.Path)
	if err != nil {
		return nil, err
	}
	pattern := req.Pattern
	if req.CaseInsensitive {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	if req.Glob != "" {
		if _, err = doublestar.Match(req.Glob, "test"); err != nil {
			return nil, err
		}
	}
	var ext string
	if req.FileType != "" {
		var ok bool
		ext, ok = fileExt[strings.ToLower(req.FileType)]
		if !ok {
			return nil, fmt.Errorf("不支持的文件类型: %s", req.FileType)
		}
	}
	out := make([]filesystem.GrepMatch, 0)
	bytesLeft := 1 << 20 // 限制汇总结果体积
	err = b.walk(ctx, rel, func(p string, info os.FileInfo) error {
		if !info.Mode().IsRegular() {
			return nil
		}
		if ext != "" && path.Ext(p) != ext {
			return nil
		}
		if req.Glob != "" {
			ok, e := doublestar.Match(req.Glob, p)
			if e != nil {
				return e
			}
			if !ok {
				ok, e = doublestar.Match(req.Glob, path.Base(p))
				if e != nil {
					return e
				}
			}
			if !ok {
				return nil
			}
		}
		if info.Size() > maxFileBytes {
			return nil
		}
		data, err := b.readFile(p)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return err
		}
		scanner := bufio.NewScanner(bytes.NewReader(data))
		scanner.Buffer(make([]byte, 64*1024), maxFileBytes+1)
		for i := 1; scanner.Scan(); i++ {
			if err := ctx.Err(); err != nil {
				return err
			}
			line := scanner.Text()
			if re.MatchString(line) {
				if len(line) > bytesLeft {
					return fmt.Errorf("grep 结果体积超限")
				}
				bytesLeft -= len(line)
				out = append(out, filesystem.GrepMatch{Path: filepath.Join(b.workspace, p), Line: i, Content: line})
				if len(out) >= maxMatches {
					return fmt.Errorf("grep 命中超限")
				}
			}
		}
		return scanner.Err()
	})
	return out, err
}

func (b *Backend) GlobInfo(ctx context.Context, req *filesystem.GlobInfoRequest) ([]filesystem.FileInfo, error) {
	if req == nil || req.Pattern == "" {
		return nil, ErrDenied
	}
	rel, err := b.resolve(req.Path)
	if err != nil {
		return nil, err
	}
	if _, err := doublestar.Match(req.Pattern, "test"); err != nil {
		return nil, err
	}
	out := make([]filesystem.FileInfo, 0)
	err = b.walk(ctx, rel, func(p string, info os.FileInfo) error {
		if p == rel {
			return nil
		}
		local := strings.TrimPrefix(strings.TrimPrefix(p, rel), "/")
		if rel == "." {
			local = p
		}
		ok, err := doublestar.Match(req.Pattern, local)
		if err != nil {
			return err
		}
		if ok {
			if len(out) >= maxMatches {
				return fmt.Errorf("glob 命中超限")
			}
			out = append(out, filesystem.FileInfo{
				Path: filepath.Join(b.workspace, p), IsDir: info.IsDir(), Size: info.Size(),
				ModifiedAt: info.ModTime().UTC().Format("2006-01-02T15:04:05Z07:00"),
			})
		}
		return nil
	})
	return out, err
}

// Execute 提供同步执行能力：直接复用 ExecuteStreaming 的隔离、超时、
// 输出上限及退出码处理，避免同步/流式实现出现安全策略分叉。
func (b *Backend) Execute(ctx context.Context, req *filesystem.ExecuteRequest) (*filesystem.ExecuteResponse, error) {
	stream, err := b.ExecuteStreaming(ctx, req)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	return collectExecuteStream(stream)
}

// ExecuteStreaming 仅通过操作系统级沙箱执行命令，不允许退回宿主机执行。
func (b *Backend) ExecuteStreaming(ctx context.Context, req *filesystem.ExecuteRequest) (*schema.StreamReader[*filesystem.ExecuteResponse], error) {
	if req == nil || strings.TrimSpace(req.Command) == "" {
		return nil, ErrDenied
	}
	if req.RunInBackendGround {
		return nil, errors.New("安全沙箱暂不支持后台任务")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if b.validateCommand != nil {
		if err := b.validateCommand(req.Command); err != nil {
			return nil, err
		}
	}
	rootInfo, err := b.root.Stat(".")
	if err != nil {
		return nil, fmt.Errorf("工作区不可用: %w", err)
	}
	pathInfo, err := os.Stat(b.workspace)
	if err != nil {
		return nil, fmt.Errorf("工作区不可用: %w", err)
	}
	if !os.SameFile(rootInfo, pathInfo) {
		return nil, fmt.Errorf("%w: 工作区目录已被替换", ErrDenied)
	}
	runCtx, cancel := context.WithTimeout(ctx, b.shellTimeout)
	cmd, cleanup, err := platformCommand(runCtx, b.workspace, req.Command, b.allowNetwork)
	if err != nil {
		err = errors.Join(err, runCtx.Err())
		cancel()
		return nil, err
	}

	// 直接连接系统管道，避免 os/exec 的复制协程与 io.Pipe 相互阻塞。
	pr, pw, err := os.Pipe()
	if err != nil {
		err = errors.Join(err, cleanup())
		cancel()
		return nil, err
	}
	if err := cmd.Start(pw); err != nil {
		pr.Close()
		pw.Close()
		err = errors.Join(err, cleanup(), runCtx.Err())
		cancel()
		return nil, fmt.Errorf("%w: %w", ErrShellUnavailable, err)
	}
	pw.Close() // 子进程已持有写端；父进程不保留它，确保退出后能读到 EOF。
	// 主进程退出后 os/exec 不再监听 ctx；脱离进程组的后代仍可能持有写端。
	// 超时直接解除管道读取，避免等待 EOF 永久阻塞。
	stopOutput := context.AfterFunc(runCtx, func() { _ = pr.Close() })
	reader, writer := schema.Pipe[*filesystem.ExecuteResponse](16)
	done := make(chan error, 1)
	go func() {
		err := cmd.Wait()
		// 主命令正常退出也必须清理同组子进程，不能只依赖 Context 超时。
		_ = cmd.Cancel()
		done <- err
	}()
	go func() {
		defer writer.Close()
		defer pr.Close()
		defer cancel()
		defer stopOutput()
		buf := make([]byte, 16*1024)
		written, truncated := 0, false
		for {
			n, readErr := pr.Read(buf)
			if n > 0 {
				allowed := n
				if written+allowed > maxShellOutput {
					allowed = maxShellOutput - written
				}
				if allowed > 0 {
					// 消费端提前 Close 时停止整个进程组，避免继续运行无人读取的命令。
					if writer.Send(&filesystem.ExecuteResponse{Output: string(buf[:allowed])}, nil) {
						_ = cmd.Cancel()
						break
					}
					written += allowed
				}
				if allowed != n {
					truncated = true
					// 达到上限立即中止整个进程组。
					_ = cmd.Cancel()
				}
			}
			if readErr != nil || truncated {
				break
			}
		}
		waitErr := <-done
		// 返回结束事件前完成资源撤销；错误路径同样不能遗留 ACL/Profile。
		cleanupErr := cleanup()
		// Context 取消/超时应作为错误传递，不伪装为正常非零退出码。
		if err := runCtx.Err(); err != nil && !truncated {
			writer.Send(nil, errors.Join(fmt.Errorf("沙箱执行中断: %w", err), cleanupErr))
			return
		}
		if cleanupErr != nil {
			writer.Send(nil, fmt.Errorf("沙箱资源清理失败: %w", cleanupErr))
			return
		}
		if truncated {
			code := -1
			writer.Send(&filesystem.ExecuteResponse{ExitCode: &code, Truncated: true}, nil)
			return
		}
		result, err := commandResult(waitErr)
		if err != nil {
			writer.Send(nil, err)
			return
		}
		// 信号诊断同样计入输出上限。
		if remaining := maxShellOutput - written; len(result.Output) > remaining {
			result.Output = result.Output[:remaining]
			result.Truncated = true
		}
		writer.Send(result, nil)
	}()
	return reader, nil
}

// resolve 检查实际绝对路径位于配置工作区内，再转换为 Root 相对路径。
// 这里只做词法检查；符号链接逃逸由 os.Root 阻止。
func (b *Backend) resolve(p string) (string, error) {
	if p == "" {
		return ".", nil // ls/glob/grep 的默认目录是配置的工作区。
	}
	denied := fmt.Errorf("%w: %q（工作区 %q）", ErrDenied, p, b.root.Name())
	for _, part := range strings.Split(filepath.ToSlash(p), "/") {
		if part == ".." {
			return "", denied
		}
	}
	var rel string
	if filepath.IsAbs(p) {
		// root.Name 保留配置路径（可能含链接），workspace 是物理路径。
		for _, root := range []string{b.root.Name(), b.workspace} {
			local, err := filepath.Rel(root, p)
			if err == nil && filepath.IsLocal(local) {
				rel = filepath.ToSlash(local)
				break
			}
		}
	}
	if rel == "" || strings.ContainsAny(rel, "\\:\x00") || strings.Contains(rel, "//") {
		return "", denied
	}
	rel = path.Clean(rel)
	if _, err := filepath.Localize(rel); err != nil {
		return "", denied // Windows 设备名、ADS 等由标准库统一拒绝
	}
	if runtime.GOOS == "windows" {
		for _, part := range strings.Split(rel, "/") {
			if part != "." && (strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ")) {
				return "", denied
			}
		}
	}
	return rel, nil
}

// readFile 统一限制文件类型和实际读取量，并在返回前关闭句柄。
func (b *Backend) readFile(rel string) ([]byte, error) {
	f, err := openRootRead(b.root, rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxFileBytes {
		return nil, ErrDenied
	}
	data, err := io.ReadAll(io.LimitReader(f, maxFileBytes+1))
	if len(data) > maxFileBytes {
		return nil, ErrDenied
	}
	return data, err
}

// put 使用 Root 内同目录临时文件及 Rename 原子替换，避免半写文件。
func (b *Backend) put(rel string, data []byte) error {
	if rel == "." || len(data) > maxFileBytes {
		return ErrDenied
	}
	mode := os.FileMode(0644)
	if info, err := b.root.Lstat(rel); err == nil {
		if !info.Mode().IsRegular() {
			return ErrDenied
		}
		mode = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	parent := path.Dir(rel)
	if err := b.root.MkdirAll(parent, 0755); err != nil {
		return err
	}
	tmp := path.Join(parent, ".sandboxfs-"+rand.Text())
	f, err := b.root.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode.Perm())
	if err != nil {
		return err
	}
	defer b.root.Remove(tmp)
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Chmod(mode.Perm()); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return b.root.Rename(tmp, rel)
}

// walk 只用 os.Root 目录句柄遍历，绝不调用宿主机的 filepath.WalkDir。
// 统一跳过符号链接和非常规文件；有上限且支持 Context 取消。
func (b *Backend) walk(ctx context.Context, start string, visit func(string, os.FileInfo) error) error {
	count := 0
	var rec func(string) error
	rec = func(rel string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		f, err := openRootRead(b.root, rel)
		if err != nil {
			return err
		}
		info, err := f.Stat()
		if err != nil {
			f.Close()
			return err
		}
		if !info.IsDir() {
			f.Close()
			if !info.Mode().IsRegular() {
				return ErrDenied
			}
			return visit(rel, info)
		}
		entries, err := f.ReadDir(maxWalkEntries + 1)
		f.Close()
		if err != nil && err != io.EOF {
			return err
		}
		if len(entries) > maxWalkEntries-count {
			return fmt.Errorf("遍历文件数量超限")
		}
		for _, e := range entries {
			count++
			if count > maxWalkEntries {
				return fmt.Errorf("遍历文件数量超限")
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if e.Type()&os.ModeSymlink != 0 {
				continue
			}
			child := path.Join(rel, e.Name())
			// 使用 Root.Lstat；不跟随最后一级链接。
			childInfo, err := b.root.Lstat(child)
			if err != nil {
				return err
			}
			if childInfo.Mode()&os.ModeSymlink != 0 {
				continue
			}
			if !childInfo.IsDir() && !childInfo.Mode().IsRegular() {
				continue
			}
			if err := visit(child, childInfo); err != nil {
				return err
			}
			if childInfo.IsDir() {
				if err := rec(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return rec(start)
}

var fileExt = map[string]string{
	"go": ".go", "py": ".py", "js": ".js", "ts": ".ts", "tsx": ".tsx",
	"rs": ".rs", "rust": ".rs", "java": ".java", "json": ".json",
	"md": ".md", "yaml": ".yaml", "yml": ".yml", "sh": ".sh",
}

// collectExecuteStream 将流式结果合并为一个响应；ExitCode 以结束事件为准。
func collectExecuteStream(stream *schema.StreamReader[*filesystem.ExecuteResponse]) (*filesystem.ExecuteResponse, error) {
	out := &filesystem.ExecuteResponse{}
	var text strings.Builder
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if chunk == nil {
			continue
		}
		text.WriteString(chunk.Output)
		if chunk.ExitCode != nil {
			code := *chunk.ExitCode
			out.ExitCode = &code
		}
		out.Truncated = out.Truncated || chunk.Truncated
	}
	out.Output = text.String()
	if out.ExitCode == nil {
		return nil, errors.New("沙箱执行未返回最终退出码")
	}
	return out, nil
}

// 普通非零退出仍是 Eino 的执行结果；信号终止额外保留可读原因，
// 避免没有 stdout/stderr 时只返回一个难以排查的 -1。
func commandResult(waitErr error) (*filesystem.ExecuteResponse, error) {
	code := 0
	result := &filesystem.ExecuteResponse{ExitCode: &code}
	if waitErr == nil {
		return result, nil
	}
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		return nil, fmt.Errorf("命令执行失败: %w", waitErr)
	}
	code = exitErr.ExitCode()
	if code < 0 {
		result.Output = fmt.Sprintf("\n[沙箱进程异常终止: %s]\n", exitErr.ProcessState.String())
	} else if runtime.GOOS == "windows" && uint32(code)&0xc0000000 == 0xc0000000 {
		result.Output = fmt.Sprintf("\n[Windows 沙箱进程异常终止: NTSTATUS 0x%08X]\n", uint32(code))
	}
	return result, nil
}
