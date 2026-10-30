//go:build darwin || linux || windows

package sandboxfs

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/adk/filesystem"
	middleware "github.com/cloudwego/eino/adk/middlewares/filesystem"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

func TestNewBackendConfig(t *testing.T) {
	workspace := t.TempDir()
	file := filepath.Join(workspace, "file.txt")
	if err := os.WriteFile(file, []byte("not a directory"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		cfg  *Config
	}{
		{"missing config", nil},
		{"empty workspace", &Config{}},
		{"missing workspace", &Config{Workspace: filepath.Join(workspace, "missing")}},
		{"file workspace", &Config{Workspace: file}},
		{"timeout too long", &Config{Workspace: workspace, ShellTimeout: 5*time.Minute + time.Nanosecond}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend, err := NewBackend(context.Background(), tc.cfg)
			if err == nil {
				backend.Close()
				t.Fatal("无效配置必须被拒绝")
			}
		})
	}
	for _, timeout := range []time.Duration{0, -time.Second, 10 * time.Second, 5 * time.Minute} {
		t.Run(timeout.String(), func(t *testing.T) {
			backend, err := NewBackend(context.Background(), &Config{Workspace: workspace, ShellTimeout: timeout})
			if err != nil {
				t.Fatal(err)
			}
			defer backend.Close()
			want := timeout
			if want <= 0 {
				want = time.Minute
			}
			if backend.shellTimeout != want {
				t.Fatalf("超时配置丢失: got %v want %v", backend.shellTimeout, want)
			}
		})
	}
}

func TestNewBackendCanonicalWorkspace(t *testing.T) {
	workspace := t.TempDir()
	alias := filepath.Join(t.TempDir(), "workspace-link")
	if err := os.Symlink(workspace, alias); err != nil {
		t.Skipf("平台不支持创建测试符号链接: %v", err)
	}
	backend, err := NewBackend(context.Background(), &Config{Workspace: alias})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	want, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatal(err)
	}
	if backend.workspace != want {
		t.Fatalf("Shell 工作区未解析为文件根目录的物理路径: got %q want %q", backend.workspace, want)
	}
	if err := backend.Write(context.Background(), &filesystem.WriteRequest{FilePath: filepath.Join(backend.workspace, "shared.txt"), Content: "shared"}); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{alias, backend.workspace} {
		content, err := backend.Read(context.Background(), &filesystem.ReadRequest{FilePath: filepath.Join(root, "shared.txt")})
		if err != nil || content == nil || content.Content != "shared" {
			t.Fatalf("原始路径和物理路径应访问同一工作区: %#v, %v", content, err)
		}
	}
}

func TestBackendExecuteAfterClose(t *testing.T) {
	backend, err := NewBackend(context.Background(), &Config{Workspace: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.Close(); err != nil {
		t.Fatal(err)
	}
	req := &filesystem.ExecuteRequest{Command: "printf must-not-run"}
	if _, err := backend.Execute(context.Background(), req); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("关闭后同步命令必须被拒绝: %v", err)
	}
	if _, err := backend.ExecuteStreaming(context.Background(), req); !errors.Is(err, os.ErrClosed) {
		t.Fatalf("关闭后流式命令必须被拒绝: %v", err)
	}
}

func TestValidateCommand(t *testing.T) {
	ctx := context.Background()
	denied := errors.New("command rejected by caller")
	command := "printf must-not-run > unexpected.txt"
	calls := 0
	b, err := NewBackend(ctx, &Config{Workspace: t.TempDir(), ValidateCommand: func(got string) error {
		calls++
		if got != command {
			t.Fatalf("命令被改写: %q", got)
		}
		return denied
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	req := &filesystem.ExecuteRequest{Command: command}
	if result, err := b.Execute(ctx, req); result != nil || !errors.Is(err, denied) {
		t.Fatalf("同步命令未遵循验证结果: %#v %v", result, err)
	}
	if stream, err := b.ExecuteStreaming(ctx, req); stream != nil || !errors.Is(err, denied) {
		t.Fatalf("流式命令未遵循验证结果: %#v %v", stream, err)
	}
	if calls != 2 {
		t.Fatalf("每次调用应验证一次命令，实际 %d 次", calls)
	}
	if _, err := b.root.Stat("unexpected.txt"); !os.IsNotExist(err) {
		t.Fatalf("被拒绝的命令仍产生了文件: %v", err)
	}
}

func TestBackend(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	b, err := NewBackend(ctx, &Config{Workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	root := workspace
	file := filepath.Join(workspace, "sub", "file.txt")
	if err := b.Write(ctx, &filesystem.WriteRequest{FilePath: file, Content: "hello\nworld"}); err != nil {
		t.Fatal(err)
	}
	got, err := b.Read(ctx, &filesystem.ReadRequest{FilePath: file})
	if err != nil || got.Content != "hello\nworld" {
		t.Fatalf("read: %v %#v", err, got)
	}
	if err := b.Edit(ctx, &filesystem.EditRequest{FilePath: file, OldString: "world", NewString: "Eino"}); err != nil {
		t.Fatal(err)
	}
	matches, err := b.GrepRaw(ctx, &filesystem.GrepRequest{Path: root, Pattern: "Eino"})
	if err != nil || len(matches) != 1 || matches[0].Path != filepath.Join(b.workspace, "sub", "file.txt") {
		t.Fatalf("grep: %v %#v", err, matches)
	}
	found, err := b.GlobInfo(ctx, &filesystem.GlobInfoRequest{Path: root, Pattern: "**/*.txt"})
	if err != nil || len(found) != 1 || found[0].Path != matches[0].Path {
		t.Fatalf("glob: %v %#v", err, found)
	}
	if content, err := b.Read(ctx, &filesystem.ReadRequest{FilePath: found[0].Path}); err != nil || content.Content != "hello\nEino" {
		t.Fatalf("搜索返回的实际路径无法直接读取: %#v %v", content, err)
	}
	listed, err := b.LsInfo(ctx, &filesystem.LsInfoRequest{Path: root})
	if err != nil || len(listed) != 1 || listed[0].Path != filepath.Join(b.workspace, "sub") {
		t.Fatalf("ls: %v %#v", err, listed)
	}
	defaultList, err := b.LsInfo(ctx, &filesystem.LsInfoRequest{})
	if err != nil || len(defaultList) != 1 || defaultList[0].Path != listed[0].Path {
		t.Fatalf("默认目录不是配置工作区: %#v %v", defaultList, err)
	}
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(workspace, "escape")); err == nil {
		escape := filepath.Join(workspace, "escape")
		if _, err := b.Read(ctx, &filesystem.ReadRequest{FilePath: escape}); err == nil {
			t.Fatal("symlink escaped root")
		}
	}
}

// 直接调用 Eino 中间件注册的 ls，复现 Agent 传入原生绝对路径的场景。
func TestEinoLsNativeWorkspace(t *testing.T) {
	ctx := context.Background()
	workspace := t.TempDir()
	b, err := NewBackend(ctx, &Config{Workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if err := b.Write(ctx, &filesystem.WriteRequest{FilePath: filepath.Join(workspace, "hello.txt"), Content: "hello"}); err != nil {
		t.Fatal(err)
	}
	m, err := middleware.New(ctx, &middleware.MiddlewareConfig{Backend: b})
	if err != nil {
		t.Fatal(err)
	}
	_, run, err := m.BeforeAgent(ctx, &adk.ChatModelAgentContext{})
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range run.Tools {
		info, err := candidate.Info(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if info.Name == "ls" {
			args, _ := json.Marshal(map[string]string{"path": workspace})
			result, err := candidate.(tool.InvokableTool).InvokableRun(ctx, string(args))
			if err != nil || result != filepath.Join(b.workspace, "hello.txt") {
				t.Fatalf("Eino ls 原生路径调用失败: %q %v", result, err)
			}
			return
		}
	}
	t.Fatal("Eino 未注册 ls 工具")
}

func TestReadPreservesEmptyLines(t *testing.T) {
	ctx := context.Background()
	backend, err := NewBackend(ctx, &Config{Workspace: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	for _, tc := range []struct {
		input         string
		offset, limit int
		want          string
	}{
		{"\n\nalpha\n", 1, 0, "\n\nalpha"},
		{"\n\n", 1, 0, "\n"},
		{"\nfirst\n\nlast", 2, 2, "first\n"},
	} {
		if err := backend.Write(ctx, &filesystem.WriteRequest{FilePath: filepath.Join(backend.workspace, "lines.txt"), Content: tc.input}); err != nil {
			t.Fatal(err)
		}
		got, err := backend.Read(ctx, &filesystem.ReadRequest{FilePath: filepath.Join(backend.workspace, "lines.txt"), Offset: tc.offset, Limit: tc.limit})
		if err != nil || got == nil || got.Content != tc.want {
			t.Fatalf("空行丢失: input=%q got=%#v err=%v", tc.input, got, err)
		}
	}
}

func TestReplacedWorkspaceRejectsCommands(t *testing.T) {
	base := t.TempDir()
	workspace := filepath.Join(base, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	backend, err := NewBackend(context.Background(), &Config{Workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	if err := os.Rename(workspace, filepath.Join(base, "old")); err != nil {
		t.Skipf("平台拒绝移动打开的工作区: %v", err)
	}
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	req := &filesystem.ExecuteRequest{Command: "printf must-not-run"}
	if _, err := backend.Execute(context.Background(), req); !errors.Is(err, ErrDenied) {
		t.Fatalf("工作区替换后不应启动命令: %v", err)
	}
	if _, err := backend.ExecuteStreaming(context.Background(), req); !errors.Is(err, ErrDenied) {
		t.Fatalf("工作区替换后不应启动流式命令: %v", err)
	}
}

func TestResolve(t *testing.T) {
	workspace := t.TempDir()
	b, err := NewBackend(context.Background(), &Config{Workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	for _, tc := range []struct {
		input, want string
		allowed     bool
	}{
		{"", ".", true},
		{"/", "", false},
		{"/workspace", "", false},
		{"/workspace/a/b.go", "", false},
		{workspace, ".", true},
		{filepath.Join(workspace, "a", "b.go"), "a/b.go", true},
		{filepath.Join(b.workspace, "a", "b.go"), "a/b.go", true},
		{"/large_tool_result/result.txt", "", false},
		{workspace + "-sibling/file", "", false},
		{workspace + "/../secret", "", false},
		{"a/b.go", "", false},
		{"/workspace/../secret", "", false},
		{"/workspace/a/../../secret", "", false},
		{"/workspace2/a", "", false},
		{"/etc/passwd", "", false},
		{"C:\\secret", "", false},
		{"/workspace/a\\secret", "", false},
		{"/workspace/a//b", "", false},
	} {
		got, err := b.resolve(tc.input)
		if tc.allowed && (err != nil || got != tc.want) {
			t.Errorf("%q => %q,%v", tc.input, got, err)
		}
		if !tc.allowed && !errors.Is(err, ErrDenied) {
			t.Errorf("%q should be denied: %v", tc.input, err)
		}
	}
}

func TestCommandResult(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("此用例使用 /bin/sh 和 Unix 信号；Windows 由原生沙箱测试覆盖")
	}
	for _, tc := range []struct {
		name    string
		command string
		code    int
		signal  bool
	}{
		{"success", "exit 0", 0, false},
		{"nonzero exit without output", "exit 7", 7, false},
		{"signal without output", "kill -TERM $$", -1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// 使用真实进程状态，验证无输出时仍能识别退出码和信号终止。
			result, err := commandResult(exec.Command("/bin/sh", "-c", tc.command).Run())
			if err != nil {
				t.Fatal(err)
			}
			if result.ExitCode == nil || *result.ExitCode != tc.code {
				t.Fatalf("退出码不正确: %#v", result)
			}
			if tc.signal {
				if !strings.Contains(result.Output, "异常终止") || !strings.Contains(result.Output, "signal:") {
					t.Fatalf("信号终止必须保留诊断: %q", result.Output)
				}
			} else if result.Output != "" {
				t.Fatalf("普通退出不应添加输出: %q", result.Output)
			}
		})
	}
	sentinel := errors.New("wait failed")
	if _, err := commandResult(sentinel); !errors.Is(err, sentinel) {
		t.Fatalf("必须保留非退出状态错误: %v", err)
	}
}

// 测试同步接口对流式消息的合并行为，无需运行真实子进程。
func TestCollectExecuteStream(t *testing.T) {
	reader, writer := schema.Pipe[*filesystem.ExecuteResponse](8)
	code := 7
	writer.Send(&filesystem.ExecuteResponse{Output: "stdout"}, nil)
	writer.Send(&filesystem.ExecuteResponse{Output: "-stderr"}, nil)
	writer.Send(&filesystem.ExecuteResponse{ExitCode: &code, Truncated: true}, nil)
	writer.Close()
	defer reader.Close()

	got, err := collectExecuteStream(reader)
	if err != nil {
		t.Fatal(err)
	}
	if got.Output != "stdout-stderr" || got.ExitCode == nil || *got.ExitCode != code || !got.Truncated {
		t.Fatalf("流式聚合不正确: %#v", got)
	}
}

func TestCollectExecuteStreamErrors(t *testing.T) {
	t.Run("no final exit", func(t *testing.T) {
		reader, writer := schema.Pipe[*filesystem.ExecuteResponse](2)
		writer.Send(&filesystem.ExecuteResponse{Output: "partial"}, nil)
		writer.Close()
		defer reader.Close()
		_, err := collectExecuteStream(reader)
		if err == nil || !strings.Contains(err.Error(), "退出码") {
			t.Fatalf("无终态时必须报错: %v", err)
		}
	})
	t.Run("stream error", func(t *testing.T) {
		reader, writer := schema.Pipe[*filesystem.ExecuteResponse](2)
		sentinel := errors.New("read broken")
		writer.Send(nil, sentinel)
		writer.Close()
		defer reader.Close()
		_, err := collectExecuteStream(reader)
		if !errors.Is(err, sentinel) {
			t.Fatalf("必须传递底层错误: %v", err)
		}
	})
}

func smokeBackend(t *testing.T, timeout time.Duration) *Backend {
	t.Helper()
	if os.Getenv("RUN_SANDBOX_SMOKE") != "1" || (runtime.GOOS != "darwin" && runtime.GOOS != "linux") {
		t.Skip("在 macOS/Linux 上设置 RUN_SANDBOX_SMOKE=1 后运行")
	}
	workspace := filepath.Join(t.TempDir(), "workspace with spaces")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	b, err := NewBackend(context.Background(), &Config{Workspace: workspace, ShellTimeout: timeout})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = b.Close() })
	return b
}

func TestShellIsolationSmoke(t *testing.T) {
	t.Setenv("SANDBOXFS_TEST_SECRET", "host-secret-marker")
	for _, mode := range []string{"sync", "streaming"} {
		t.Run(mode, func(t *testing.T) {
			b := smokeBackend(t, 10*time.Second)
			ctx := context.Background()
			for file, content := range map[string]string{
				"hello.txt":  "workspace-okay",
				"output.txt": strings.Repeat("x", maxShellOutput),
				"create.sh":  "printf 'SCRIPT_STARTED\\n'\nprintf 'abcddddddddd\\n' > \"$1\"\n",
			} {
				if err := b.Write(ctx, &filesystem.WriteRequest{FilePath: filepath.Join(b.workspace, file), Content: content}); err != nil {
					t.Fatal(err)
				}
			}
			outside := filepath.Join(t.TempDir(), "outside.txt")
			if err := os.WriteFile(outside, []byte("host-only"), 0600); err != nil {
				t.Fatal(err)
			}
			quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
			for _, tc := range []struct {
				name, command, want string
				code                int // 正数表示指定退出码；-2 表示任意普通非零退出码
				truncated           bool
			}{
				{"read workspace", "cat hello.txt", "workspace-okay", 0, false},
				{"working directory", "pwd -P", b.workspace + "\n", 0, false},
				{"read absolute path", "cat " + quote(filepath.Join(b.workspace, "hello.txt")), "workspace-okay", 0, false},
				{"deny host read", "cat /etc/hosts", "", -2, false},
				{"exit status", "exit 7", "", 7, false},
				{"signal", "kill -TERM $$", "异常终止", -1, false},
				{"environment", "printf '%s' \"${SANDBOXFS_TEST_SECRET-unset}\"", "unset", 0, false},
				{"discard output", "printf ignored > /dev/null && printf null-okay", "null-okay", 0, false},
				{"private caches", `test -d "$HOME" && mkdir -p "$XDG_CACHE_HOME" "$npm_config_cache" "$PIP_CACHE_DIR" && printf x > "$XDG_CACHE_HOME/probe" && printf x > "$npm_config_cache/probe" && printf x > "$PIP_CACHE_DIR/probe" && printf cache-okay`, "cache-okay", 0, false},
				{"script inside", "sh " + quote(filepath.Join(b.workspace, "create.sh")) + " inside.txt", "SCRIPT_STARTED\n", 0, false},
				{"script outside create", "sh ./create.sh " + quote(outside+".new"), "SCRIPT_STARTED\n", -2, false},
				{"script outside overwrite", "sh ./create.sh " + quote(outside), "SCRIPT_STARTED\n", -2, false},
				{"exact output limit", "cat output.txt", "", 0, false},
				{"output overflow", "yes x", "", -1, true},
			} {
				t.Run(tc.name, func(t *testing.T) {
					req := &filesystem.ExecuteRequest{Command: tc.command}
					var result *filesystem.ExecuteResponse
					var err error
					if mode == "sync" {
						result, err = b.Execute(ctx, req)
					} else {
						sr, streamErr := b.ExecuteStreaming(ctx, req)
						if streamErr != nil {
							t.Fatal(streamErr)
						}
						defer sr.Close()
						result, err = collectExecuteStream(sr)
					}
					if err != nil {
						t.Fatal(err)
					}
					if result.ExitCode == nil || (tc.code == -2 && *result.ExitCode <= 0) || (tc.code != -2 && *result.ExitCode != tc.code) {
						t.Fatalf("退出码不正确: %#v", result)
					}
					if !strings.Contains(result.Output, tc.want) || result.Truncated != tc.truncated || len(result.Output) > maxShellOutput {
						t.Fatalf("输出不正确: len=%d truncated=%v", len(result.Output), result.Truncated)
					}
					if (tc.name == "exact output limit" || tc.name == "output overflow") && len(result.Output) != maxShellOutput {
						t.Fatalf("输出丢失: got %d bytes", len(result.Output))
					}
				})
			}
			inside, err := b.Read(ctx, &filesystem.ReadRequest{FilePath: filepath.Join(b.workspace, "inside.txt")})
			if err != nil || inside == nil || inside.Content != "abcddddddddd" {
				t.Fatalf("Shell 和文件工具未共用工作区: %#v %v", inside, err)
			}
			if data, err := os.ReadFile(outside); err != nil || string(data) != "host-only" {
				t.Fatalf("工作区外文件被覆盖: %q %v", data, err)
			}
			if _, err := os.Stat(outside + ".new"); !os.IsNotExist(err) {
				t.Fatalf("工作区外产生了新文件: %v", err)
			}
			if runtime.GOOS == "linux" {
				result, err := b.Execute(ctx, &filesystem.ExecuteRequest{Command: "cat /proc/1/environ"})
				if err != nil || strings.Contains(result.Output, "host-secret-marker") {
					t.Fatalf("启动器环境隔离失败: %v", err)
				}
			}
		})
	}
}

func TestShellNetworkSmoke(t *testing.T) {
	base := smokeBackend(t, 10*time.Second)
	if _, err := os.Stat("/usr/bin/curl"); err != nil {
		t.Skip("需要系统 curl 验证真实网络访问")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("network-okay"))
	}))
	defer server.Close()
	outside := filepath.Join(t.TempDir(), "host-only.txt")
	if err := os.WriteFile(outside, []byte("host-only"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		allow bool
	}{{"default denied", false}, {"explicitly allowed", true}} {
		t.Run(tc.name, func(t *testing.T) {
			b, err := NewBackend(context.Background(), &Config{Workspace: base.workspace, AllowNetwork: tc.allow, ShellTimeout: 10 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			for _, url := range []string{server.URL, strings.Replace(server.URL, "127.0.0.1", "localhost", 1)} {
				result, err := b.Execute(context.Background(), &filesystem.ExecuteRequest{
					Command: "/usr/bin/curl --noproxy '*' --silent --show-error --fail --connect-timeout 1 --max-time 2 " + url,
				})
				if err != nil || result.ExitCode == nil {
					t.Fatalf("网络测试执行异常: %#v %v", result, err)
				}
				if tc.allow {
					if *result.ExitCode != 0 || result.Output != "network-okay" {
						t.Fatalf("允许联网后仍无法访问 %s: %#v", url, result)
					}
				} else if *result.ExitCode == 0 || strings.Contains(result.Output, "network-okay") {
					t.Fatalf("默认禁网被绕过: %#v", result)
				}
			}
			// 打开网络不能顺带开放任意宿主机文件的读取或写入。
			quoted := "'" + strings.ReplaceAll(outside, "'", "'\"'\"'") + "'"
			for _, command := range []string{"cat " + quoted, "printf forbidden > " + quoted} {
				result, err := b.Execute(context.Background(), &filesystem.ExecuteRequest{Command: command})
				if err != nil || result.ExitCode == nil || *result.ExitCode == 0 {
					t.Fatalf("网络开关影响了文件隔离: %#v %v", result, err)
				}
			}
			if data, err := os.ReadFile(outside); err != nil || string(data) != "host-only" {
				t.Fatalf("宿主文件被修改: %q %v", data, err)
			}
		})
	}
}

func TestShellLifecycleSmoke(t *testing.T) {
	b := smokeBackend(t, 300*time.Millisecond)
	start := time.Now()
	if _, err := b.Execute(context.Background(), &filesystem.ExecuteRequest{Command: "sleep 10"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("必须返回超时: %v", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("超时取消过慢")
	}
	// 覆盖子进程保留输出管道和主动重定向输出两种情况。
	for _, redirect := range []string{"", " > child.log 2>&1"} {
		result, err := b.Execute(context.Background(), &filesystem.ExecuteRequest{Command: "(sleep 1; printf late > late.txt) <&-" + redirect + " &"})
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("后台进程执行错误: %v", err)
		}
		if err == nil && (result.ExitCode == nil || *result.ExitCode != 0) {
			t.Fatalf("后台进程清理失败: %#v %v", result, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sr, err := b.ExecuteStreaming(ctx, &filesystem.ExecuteRequest{Command: "printf started; sleep 1; printf late > closed-stream.txt"})
	if err != nil {
		t.Fatal(err)
	}
	first, err := sr.Recv()
	if err != nil || first == nil || first.Output != "started" {
		t.Fatalf("没有在命令结束前流式返回: %#v %v", first, err)
	}
	sr.Close()
	cancel()
	b.Close()
	time.Sleep(1200 * time.Millisecond)
	for _, file := range []string{"late.txt", "closed-stream.txt"} {
		if _, err := os.Stat(filepath.Join(b.workspace, file)); !os.IsNotExist(err) {
			t.Fatalf("命令结束后仍写入 %s: %v", file, err)
		}
	}
}

func TestDarwinDataVolumeReadSmoke(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("仅 macOS 有 Data 卷别名")
	}
	b := smokeBackend(t, 5*time.Second)
	outside, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outside = filepath.Join(outside, "host-only.txt")
	if err := os.WriteFile(outside, []byte("host-only-marker"), 0600); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join("/System/Volumes/Data", outside)
	if _, err := os.Stat(alias); err != nil {
		t.Skipf("系统没有可访问的 Data 卷别名: %v", err)
	}
	result, err := b.Execute(context.Background(), &filesystem.ExecuteRequest{
		Command: "cat '" + strings.ReplaceAll(alias, "'", "'\"'\"'") + "'",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode == nil || *result.ExitCode == 0 || strings.Contains(result.Output, "host-only-marker") {
		t.Fatalf("通过 Data 卷别名读取了工作区外文件: %#v", result)
	}
}

func TestDarwinTemporaryPathsSmoke(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("仅 macOS 使用宿主临时目录")
	}
	b := smokeBackend(t, 5*time.Second)
	t.Chdir(t.TempDir())
	if err := os.Mkdir("tmp", 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("tmp", "tmp-alias"); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"tmp", "tmp-alias"} {
		t.Run(dir, func(t *testing.T) {
			t.Setenv("TMPDIR", dir)
			result, err := b.Execute(context.Background(), &filesystem.ExecuteRequest{
				Command: `mkdir -p "$XDG_CACHE_HOME" && printf okay > "$TMPDIR/probe" && cat "$TMPDIR/probe"`,
			})
			if err != nil || result.ExitCode == nil || *result.ExitCode != 0 || result.Output != "okay" {
				t.Fatalf("相对路径或符号链接临时目录不可用: %#v %v", result, err)
			}
		})
	}
}

// 脱离原进程组的后代可能继续持有 stdout；即使主进程已退出，读取也必须服从超时。
func TestInheritedOutputDeadlineSmoke(t *testing.T) {
	b := smokeBackend(t, 500*time.Millisecond)
	if _, err := os.Stat("/usr/bin/perl"); err != nil {
		t.Skip("需要系统 Perl 构造脱离进程组的后代")
	}
	start := time.Now()
	result, err := b.Execute(context.Background(), &filesystem.ExecuteRequest{
		// 子进程只保留输出管道并等待两秒，不读写文件；父进程收到就绪标记后退出。
		Command: `/usr/bin/perl -MPOSIX -e 'pipe(my $r, my $w) or die $!; my $pid = fork; defined($pid) or die $!; if (!$pid) { close $r; my $sid = POSIX::setsid(); defined($sid) && $sid >= 0 or die "setsid: $!"; print $w "ready"; close $w; sleep 2; POSIX::_exit(0); } close $w; local $/; my $ready = <$r>; defined($ready) && $ready eq "ready" or die "child did not detach"; print "parent-exited\n";'`,
	})
	// Linux 的 PID Namespace 可以在超时前直接清理后代；两种情况都不能等满两秒。
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("输出管道等待异常: %v", err)
	}
	if err == nil && (result.ExitCode == nil || *result.ExitCode != 0 || !strings.Contains(result.Output, "parent-exited")) {
		t.Fatalf("后代未按预期脱离进程组: %#v", result)
	}
	if elapsed := time.Since(start); elapsed > 1500*time.Millisecond {
		t.Fatalf("主进程退出后的管道读取没有及时取消: %v", elapsed)
	}
}

func TestSpecialFilesDoNotBlock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 没有 Unix FIFO")
	}
	workspace := t.TempDir()
	fifo := filepath.Join(workspace, "pipe")
	if err := exec.Command("mkfifo", fifo).Run(); err != nil {
		t.Fatal(err)
	}
	backend, err := NewBackend(context.Background(), &Config{Workspace: workspace})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	for name, operation := range map[string]func(context.Context) error{
		"read": func(ctx context.Context) error {
			_, err := backend.Read(ctx, &filesystem.ReadRequest{FilePath: filepath.Join(backend.workspace, "pipe")})
			return err
		},
		"ls": func(ctx context.Context) error {
			_, err := backend.LsInfo(ctx, &filesystem.LsInfoRequest{Path: filepath.Join(backend.workspace, "pipe")})
			return err
		},
		"grep": func(ctx context.Context) error {
			_, err := backend.GrepRaw(ctx, &filesystem.GrepRequest{Path: filepath.Join(backend.workspace, "pipe"), Pattern: "x"})
			return err
		},
		"glob": func(ctx context.Context) error {
			_, err := backend.GlobInfo(ctx, &filesystem.GlobInfoRequest{Path: filepath.Join(backend.workspace, "pipe"), Pattern: "*"})
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- operation(ctx) }()
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("特殊文件必须被拒绝")
				}
			case <-time.After(2 * time.Second):
				// 若发生回归，解除 FIFO 阻塞，避免测试自身遗留阻塞协程。
				rescue, _ := os.OpenFile(fifo, os.O_RDWR, 0)
				if rescue != nil {
					defer rescue.Close()
				}
				select {
				case <-done:
				case <-time.After(time.Second):
				}
				t.Fatal("特殊文件打开不应等待 FIFO 写入方")
			}
		})
	}
}

func TestGrepLimitClosesFiles(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("此检查使用 /dev/fd")
	}
	backend, err := NewBackend(context.Background(), &Config{Workspace: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	if err := backend.Write(context.Background(), &filesystem.WriteRequest{FilePath: filepath.Join(backend.workspace, "hits.txt"), Content: strings.Repeat("match\n", 1001)}); err != nil {
		t.Fatal(err)
	}
	if err := backend.Write(context.Background(), &filesystem.WriteRequest{FilePath: filepath.Join(backend.workspace, "empty.txt")}); err != nil {
		t.Fatal(err)
	}
	// 先初始化运行时的 I/O 设施，避免将它们的持久描述符误判为泄漏。
	if _, err := backend.GrepRaw(context.Background(), &filesystem.GrepRequest{Path: filepath.Join(backend.workspace, "empty.txt"), Pattern: "match"}); err != nil {
		t.Fatal(err)
	}
	// 禁止 GC 掩盖文件生命周期错误；文件必须由调用明确关闭。
	old := debug.SetGCPercent(-1)
	defer debug.SetGCPercent(old)
	count := func() int {
		entries, err := os.ReadDir("/dev/fd")
		if err != nil {
			t.Fatal(err)
		}
		return len(entries)
	}
	before := count()
	for i := 0; i < 5; i++ {
		_, err := backend.GrepRaw(context.Background(), &filesystem.GrepRequest{Path: filepath.Join(backend.workspace, "hits.txt"), Pattern: "match"})
		if err == nil || !strings.Contains(err.Error(), "命中超限") {
			t.Fatalf("未触发预期的超限分支: %v", err)
		}
		if _, err := backend.GrepRaw(context.Background(), &filesystem.GrepRequest{Path: filepath.Join(backend.workspace, "empty.txt"), Pattern: "match"}); err != nil {
			t.Fatal(err)
		}
	}
	if after := count(); after != before {
		t.Fatalf("grep 超限泄漏文件描述符: before=%d after=%d", before, after)
	}
}

func TestLinuxLauncherEnvironment(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("仅在 linux 上运行")
	}
	t.Setenv("SANDBOXFS_HOST_SECRET", "must-not-inherit")
	t.Setenv("LD_PRELOAD", "/must-not-inherit.so")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	workspace := t.TempDir()
	command := "printf '%s' '--bind / /host'"
	for name, allow := range map[string]bool{"denied": false, "allowed": true} {
		t.Run(name, func(t *testing.T) {
			process, cleanup, err := platformCommand(ctx, workspace, command, allow)
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			cmd := process.(execCommand).Cmd
			for _, flag := range []string{"--unshare-all", "--die-with-parent", "--new-session", "--clearenv"} {
				if !slices.Contains(cmd.Args, flag) {
					t.Fatalf("缺少隔离参数: %s", flag)
				}
			}
			if slices.Contains(cmd.Args, "--share-net") != allow || slices.Contains(cmd.Args, "/etc/resolv.conf") != allow {
				t.Fatalf("网络隔离或 DNS 挂载不符合配置: %q", cmd.Args)
			}
			bind := slices.Index(cmd.Args, "--bind")
			if bind < 0 || !slices.Equal(cmd.Args[bind+1:bind+3], []string{workspace, workspace}) ||
				!slices.Equal(cmd.Args[len(cmd.Args)-4:], []string{"--", "/bin/sh", "-c", command}) {
				t.Fatalf("工作区映射或命令参数错误: %q", cmd.Args)
			}
			if cmd.Env == nil || cmd.Dir != "/" {
				t.Fatal("启动器必须显式设置最小环境和工作目录")
			}
			for _, value := range cmd.Environ() {
				if strings.Contains(value, "must-not-inherit") || strings.HasPrefix(value, "LD_") {
					t.Fatalf("启动器继承了宿主机环境: %q", value)
				}
			}
		})
	}
}

func TestWindowsInvalidCommandFailClosed(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("仅在 windows 上运行")
	}
	for name, allow := range map[string]bool{"network denied": false, "network allowed": true} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			backend, err := NewBackend(ctx, &Config{Workspace: t.TempDir(), AllowNetwork: allow})
			if err != nil {
				t.Fatal(err)
			}
			defer backend.Close()
			if err := backend.Write(ctx, &filesystem.WriteRequest{FilePath: filepath.Join(backend.workspace, "ordinary.txt"), Content: "files still work"}); err != nil {
				t.Fatal(err)
			}
			req := &filesystem.ExecuteRequest{Command: "echo must-not-run\x00"}
			if result, err := backend.Execute(ctx, req); result != nil || !errors.Is(err, ErrDenied) {
				t.Fatalf("Windows 不得执行包含 NUL 的命令: result=%v err=%v", result, err)
			}
			if stream, err := backend.ExecuteStreaming(ctx, req); stream != nil || !errors.Is(err, ErrDenied) {
				t.Fatalf("Windows 不得启动无效流式命令: stream=%v err=%v", stream, err)
			}
			if cmd, cleanup, err := platformCommand(ctx, backend.workspace, req.Command, allow); cmd != nil || cleanup != nil || !errors.Is(err, ErrDenied) {
				t.Fatal("Windows 底层实现也必须拒绝启动进程")
			}
			if cmd, cleanup, err := platformCommand(ctx, `\\server\share`, "echo must-not-run", allow); cmd != nil || cleanup != nil || !errors.Is(err, ErrShellUnavailable) {
				t.Fatal("不支持的工作区不得退回普通执行")
			}
		})
	}
}

func TestWindowsSpecialFilePaths(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("仅在 windows 上运行")
	}
	ctx := context.Background()
	backend, err := NewBackend(ctx, &Config{Workspace: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	for _, name := range []string{"NUL", "CON", "COM¹", "file.txt:stream", "alias.", "alias "} {
		p := filepath.Join(backend.workspace, name)
		err := backend.Write(ctx, &filesystem.WriteRequest{FilePath: p, Content: "must-not-write"})
		if !errors.Is(err, ErrDenied) {
			t.Errorf("必须在打开文件前拒绝 %q: %v", p, err)
		}
	}
}

// 只在 Windows 显式运行；探针使用真实系统 API，不能用参数断言替代隔离验证。
func TestWindowsShellSmoke(t *testing.T) {
	if runtime.GOOS != "windows" || os.Getenv("RUN_SANDBOX_SMOKE") != "1" {
		t.Skip("在 Windows 上设置 RUN_SANDBOX_SMOKE=1 后运行")
	}
	t.Setenv("SANDBOXFS_TEST_SECRET", "must-not-inherit")
	workspace := filepath.Join(t.TempDir(), "workspace 空 格 &")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "probe.go")
	if err := os.WriteFile(source, []byte(windowsProbeSource), 0600); err != nil {
		t.Fatal(err)
	}
	probe := filepath.Join(workspace, "probe.exe")
	build := exec.Command("go", "build", "-mod=readonly", "-o", probe, source)
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("编译探针失败: %v\n%s", err, output)
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("host-only"), 0600); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"inside.txt": "inside-data", "script with spaces.cmd": "@echo off\r\necho batch-okay\r\n"} {
		if err := os.WriteFile(filepath.Join(workspace, name), []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	quote := func(s string) string { return `"` + s + `"` }
	prefix := quote(probe) + " "
	linked := os.Symlink(filepath.Dir(outside), filepath.Join(workspace, "outside-link")) == nil
	packageSID := regexp.MustCompile(`S-1-15-2-[0-9]+-[0-9]+`)
	for name, allow := range map[string]bool{"network denied": false, "network allowed": true} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			b, err := NewBackend(ctx, &Config{Workspace: workspace, AllowNetwork: allow, ShellTimeout: 15 * time.Second})
			if err != nil {
				t.Fatal(err)
			}
			defer b.Close()
			caps := "0"
			if allow {
				caps = "3"
			}
			for _, tc := range []struct {
				name, command, want string
				code                int
			}{
				{"LPAC token", prefix + "token", "1 1 " + caps, 0},
				{"read", prefix + "read inside.txt", "inside-data", 0},
				{"write unicode", prefix + `write created.txt "你好 & welcome"`, "", 0},
				{"batch quoting", `call "script with spaces.cmd"`, "batch-okay", 0},
				{"environment", prefix + "env", "private-environment", 0},
				{"exit status", "exit /b 7", "", 7},
				{"deny read", prefix + "read " + quote(outside), "", 1},
				{"deny write", prefix + "write " + quote(outside) + " forbidden", "", 1},
				{"deny create", prefix + "write " + quote(outside+".new") + " forbidden", "", 1},
				{"replace", prefix + "replace inside.txt replaced", "", 0},
			} {
				t.Run(tc.name, func(t *testing.T) {
					// 流式路径也必须使用同一原生启动器。
					sr, err := b.ExecuteStreaming(ctx, &filesystem.ExecuteRequest{Command: tc.command})
					if err != nil {
						t.Fatal(err)
					}
					defer sr.Close()
					result, err := collectExecuteStream(sr)
					if err != nil || result.ExitCode == nil || *result.ExitCode != tc.code || !strings.Contains(result.Output, tc.want) {
						t.Fatalf("Windows 命令结果不正确: %#v %v", result, err)
					}
				})
			}
			if linked {
				result, err := b.Execute(ctx, &filesystem.ExecuteRequest{Command: prefix + `read "outside-link\outside.txt"`})
				if err != nil || result.ExitCode == nil || *result.ExitCode != 1 {
					t.Fatalf("链接打开了外部文件: %#v %v", result, err)
				}
			}
			data, err := b.Read(ctx, &filesystem.ReadRequest{FilePath: filepath.Join(b.workspace, "created.txt")})
			if err != nil || data.Content != "你好 & welcome" {
				t.Fatalf("Shell 与文件工具工作区不一致: %#v %v", data, err)
			}
			result, err := b.Execute(ctx, &filesystem.ExecuteRequest{Command: prefix + "output"})
			if err != nil || !result.Truncated || len(result.Output) != maxShellOutput {
				t.Fatalf("输出限制失效: %#v %v", result, err)
			}
			if _, err := b.Execute(ctx, &filesystem.ExecuteRequest{Command: prefix + "spawn late.txt"}); err != nil {
				t.Fatal(err)
			}
			time.Sleep(1500 * time.Millisecond)
			if _, err := os.Stat(filepath.Join(workspace, "late.txt")); !os.IsNotExist(err) {
				t.Fatalf("Job 未清理后代: %v", err)
			}
			// 默认禁网不能只检查 Token；验证真实 TCP 连接被拒绝。
			server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
			defer server.Close()
			if !allow {
				result, err := b.Execute(ctx, &filesystem.ExecuteRequest{Command: prefix + "tcp " + strings.TrimPrefix(server.URL, "http://")})
				if err != nil || result.ExitCode == nil || *result.ExitCode != 1 {
					t.Fatalf("默认禁网失效: %#v %v", result, err)
				}
			} else if target := os.Getenv("SANDBOXFS_WINDOWS_NETWORK_TARGET"); target != "" {
				result, err := b.Execute(ctx, &filesystem.ExecuteRequest{Command: prefix + "tcp " + quote(target)})
				if err != nil || result.ExitCode == nil || *result.ExitCode != 0 {
					t.Fatalf("网络能力未生效: %#v %v", result, err)
				}
			}
			// 正常结束后不应遗留本次 AppContainer SID 的 ACL。
			if output, err := exec.Command(probe, "acl", workspace).CombinedOutput(); err != nil || packageSID.Match(output) {
				t.Fatalf("临时权限没有撤销: %s %v", output, err)
			}
			// 两个网络模式共用测试目录，恢复下一轮需要的内容。
			if err := os.WriteFile(filepath.Join(workspace, "inside.txt"), []byte("inside-data"), 0600); err != nil {
				t.Fatal(err)
			}
		})
	}
	if data, err := os.ReadFile(outside); err != nil || string(data) != "host-only" {
		t.Fatalf("宿主文件被修改: %q %v", data, err)
	}
	if _, err := os.Stat(outside + ".new"); !os.IsNotExist(err) {
		t.Fatalf("宿主文件被创建: %v", err)
	}
	b, err := NewBackend(context.Background(), &Config{Workspace: workspace, ShellTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	start := time.Now()
	if _, err := b.Execute(context.Background(), &filesystem.ExecuteRequest{Command: prefix + "sleep"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("超时未传递: %v", err)
	}
	if time.Since(start) > 6*time.Second {
		t.Fatal("Windows 超时取消过慢")
	}
	if output, err := exec.Command(probe, "acl", workspace).CombinedOutput(); err != nil || packageSID.Match(output) {
		t.Fatalf("超时后权限没有撤销: %s %v", output, err)
	}
	b.shellTimeout = 15 * time.Second
	ctx, cancel := context.WithCancel(context.Background())
	sr, err := b.ExecuteStreaming(ctx, &filesystem.ExecuteRequest{Command: prefix + "hold cancelled.txt"})
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	first, err := sr.Recv()
	if err != nil || first == nil || first.Output != "started" {
		sr.Close()
		cancel()
		t.Fatalf("未能在命令结束前读取流: %#v %v", first, err)
	}
	sr.Close()
	cancel()
	time.Sleep(1500 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(workspace, "cancelled.txt")); !os.IsNotExist(err) {
		t.Fatalf("流关闭后命令仍写入文件: %v", err)
	}
	// 中途准备失败也必须撤销此前已经添加的权限。
	bad := filepath.Join(workspace, "null-dacl.txt")
	if err := os.WriteFile(bad, nil, 0600); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(probe, "nullacl", bad).CombinedOutput(); err != nil {
		t.Fatalf("准备失败场景: %s %v", output, err)
	}
	if _, err := b.Execute(context.Background(), &filesystem.ExecuteRequest{Command: "echo must-not-run"}); !errors.Is(err, ErrShellUnavailable) {
		t.Fatalf("不安全的 ACL 不得启动命令: %v", err)
	}
	if output, err := exec.Command(probe, "acl", workspace).CombinedOutput(); err != nil || packageSID.Match(output) {
		t.Fatalf("准备失败后权限没有撤销: %s %v", output, err)
	}
}

const windowsProbeSource = `package main
import (
    "bytes"
    "fmt"
    "net"
    "os"
    "os/exec"
    "path/filepath"
    "time"
    "unsafe"
    "golang.org/x/sys/windows"
)
func check(err error) { if err != nil { fmt.Fprintln(os.Stderr, err); os.Exit(1) } }
func main() {
    switch os.Args[1] {
    case "token":
        values := []uint32{0,0,0}
        for i, class := range []uint32{29,46,30} {
            var size uint32
            _ = windows.GetTokenInformation(windows.GetCurrentProcessToken(), class, nil, 0, &size)
            data := make([]byte, size)
            check(windows.GetTokenInformation(windows.GetCurrentProcessToken(), class, &data[0], size, &size))
            values[i] = *(*uint32)(unsafe.Pointer(&data[0]))
        }
        fmt.Printf("%d %d %d", values[0],values[1],values[2])
    case "read":
        data, err := os.ReadFile(os.Args[2]); check(err); _, err = os.Stdout.Write(data); check(err)
    case "write":
        check(os.WriteFile(os.Args[2], []byte(os.Args[3]), 0600))
    case "replace":
        check(os.WriteFile(os.Args[2]+".tmp", []byte(os.Args[3]), 0600))
        check(os.Rename(os.Args[2]+".tmp", os.Args[2]))
    case "env":
        if os.Getenv("SANDBOXFS_TEST_SECRET") != "" { os.Exit(1) }; fmt.Print("private-environment")
    case "output":
        _, err := os.Stdout.Write(bytes.Repeat([]byte("x"), (1<<20)+100)); check(err)
    case "sleep":
        time.Sleep(10*time.Second)
    case "hold":
        fmt.Print("started"); time.Sleep(time.Second); check(os.WriteFile(os.Args[2], []byte("escaped"), 0600))
    case "spawn":
        cmd := exec.Command(os.Args[0], "late", os.Args[2]); cmd.Stdout,cmd.Stderr = os.Stdout,os.Stderr; check(cmd.Start())
    case "late":
        time.Sleep(time.Second); check(os.WriteFile(os.Args[2], []byte("escaped"), 0600))
    case "tcp":
        conn, err := net.DialTimeout("tcp", os.Args[2], 2*time.Second); check(err); conn.Close()
    case "acl":
        check(filepath.WalkDir(os.Args[2], func(p string, entry os.DirEntry, err error) error {
            if err != nil { return err }; if entry.Type()&os.ModeSymlink != 0 { return nil }
            sd, err := windows.GetNamedSecurityInfo(p, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
            if err == nil { fmt.Println(sd.String()) }; return err
        }))
    case "nullacl":
        check(windows.SetNamedSecurityInfo(os.Args[2], windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION, nil,nil,nil,nil))
    }
}
`
