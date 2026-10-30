# Eino SandboxFS

面向 [Eino ADK](https://github.com/cloudwego/eino) 的第三方文件系统后端，为 Agent 提供工作区文件操作和沙箱命令执行能力。

同一个 `*sandboxfs.Backend` 实现 `filesystem.Backend`、`filesystem.Shell` 和 `filesystem.StreamingShell`，可直接接入 Eino 的 filesystem 中间件。

## 特性

- 文件操作：支持目录列表、文本读写、内容替换、正则搜索和 Glob 匹配。
- 路径守卫：基于 Go `os.Root` 限制文件访问范围，阻止路径穿越和符号链接逃逸。
- 沙箱执行：macOS 使用 Seatbelt，Linux 使用 Bubblewrap，Windows 使用 LPAC / AppContainer（实验性）。
- 同步与流式输出：支持超时、取消、输出上限和子进程清理。
- 网络控制：命令默认禁止联网，可通过 `AllowNetwork` 显式开启。

## 快速开始

### 安装

要求 Go **1.27.1 或更新版本**。

```bash
go get github.com/sda1-hacker/eino-plugins/sandboxfs
```

本组件是 `eino-plugins` 仓库中的独立 Go module，以上命令需要公开版本已发布。指定版本时使用 `@v0.1.0`；对应仓库标签为 `sandboxfs/v0.1.0`，详见 [发布说明](https://github.com/sda1-hacker/eino-plugins/blob/main/docs/releasing.md)。

文件操作无需额外系统组件；执行命令还需满足下文的平台要求。

### 基本使用

以下示例将当前目录作为工作区：

```go
package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/cloudwego/eino/adk/filesystem"
	"github.com/sda1-hacker/eino-plugins/sandboxfs"
)

func main() {
	ctx := context.Background()
	workspace, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	backend, err := sandboxfs.NewBackend(ctx, &sandboxfs.Config{
		Workspace: workspace,
	})
	if err != nil {
		panic(err)
	}
	defer backend.Close()

	filePath := filepath.Join(workspace, "hello.txt")
	err = backend.Write(ctx, &filesystem.WriteRequest{
		FilePath: filePath,
		Content:  "Hello, Eino SandboxFS!",
	})
	if err != nil {
		panic(err)
	}

	content, err := backend.Read(ctx, &filesystem.ReadRequest{
		FilePath: filePath,
	})
	if err != nil {
		panic(err)
	}
	fmt.Println(content.Content)
}
```

工作区必须是已存在的目录。使用结束后调用 `Close()` 释放目录句柄，关闭前应先结束文件操作并取消仍在运行的命令。

### 接入 Eino

创建 Backend 后，将同一个实例传入 filesystem 中间件：

```go
import fsmiddleware "github.com/cloudwego/eino/adk/middlewares/filesystem"

middleware, err := fsmiddleware.New(ctx, &fsmiddleware.MiddlewareConfig{
	Backend:        backend,
	StreamingShell: backend,
})
if err != nil {
	return err
}
```

将 `middleware` 加入 `adk.ChatModelAgentConfig.Handlers`。仅使用文件工具时配置 `Backend` 即可；需要同步 Shell 时，将 `StreamingShell` 改为 `Shell`，两者不能同时配置。

Agent 的指令中应说明实际工作区路径，例如：

```text
你的工作区是 /absolute/path/to/project。
文件工具使用工作区内的实际绝对路径，Shell 从该工作区执行。
```

### 执行命令

```go
result, err := backend.Execute(ctx, &filesystem.ExecuteRequest{
	Command: "echo hello",
})
if err != nil {
	return err
}
fmt.Print(result.Output)
if result.ExitCode == nil || *result.ExitCode != 0 {
	return fmt.Errorf("命令执行失败，输出截断：%t", result.Truncated)
}
```

此处的 `filesystem.ExecuteRequest` 来自 `github.com/cloudwego/eino/adk/filesystem`。

`Output` 包含合并后的标准输出和标准错误。普通非零退出通过 `ExitCode` 返回，超时、取消和沙箱启动失败通过 `error` 返回，因此调用方需要同时检查两者。输出超限时会终止命令并设置 `Truncated`。

流式执行使用 `ExecuteStreaming`，调用方应持续读取并关闭返回的 reader；提前停止消费时，同时取消传入的 context。

## 配置

```go
type Config struct {
	Workspace       string
	ShellTimeout    time.Duration
	AllowNetwork    bool
	ValidateCommand func(string) error
}
```

| 字段 | 说明 | 默认值 |
| --- | --- | --- |
| `Workspace` | 文件操作和 Shell 共用的工作区，初始化时转换为绝对路径并解析符号链接 | 必填 |
| `ShellTimeout` | 每次命令的执行超时，最大 5 分钟 | 60 秒 |
| `AllowNetwork` | 是否允许 Shell 及其子进程联网 | `false` |
| `ValidateCommand` | 启动沙箱前检查原始命令，返回错误则拒绝执行 | 不额外校验 |

`AllowNetwork` 同时适用于同步和流式执行，不影响文件路径守卫，也不影响沙箱外 Agent 调用模型 API。开启后不提供域名或 IP 白名单，且不会自动继承宿主机的代理、密钥等环境变量。

`ValidateCommand` 可用于业务层的命令限制；校验器应考虑完整 Shell 语法，不能仅检查第一个单词。

## 工作区与路径

- 文件工具使用工作区内的实际绝对路径，例如 `/Users/me/project/src/main.go` 或 `C:\project\src\main.go`；列表和搜索返回的路径可直接用于后续文件操作。
- `LsInfo`、`GlobInfo`、`GrepRaw` 的空路径默认指向工作区。相对文件路径、包含 `..` 的路径和工作区外路径会被拒绝。
- Shell 的工作目录就是配置的工作区，可使用相对路径执行脚本，例如 `sh ./create.sh`；Windows 使用 `cmd.exe`，脚本及命令语法需与平台匹配。
- 工作区没有固定的虚拟路径映射，`/` 表示宿主机根目录。Eino 若将大结果卸载为文件，生成的保存路径也应位于工作区内。

## 平台支持

| 平台 | 文件操作 | Shell 实现与要求 |
| --- | --- | --- |
| macOS | 支持 | Seatbelt，依赖 `/usr/bin/sandbox-exec` |
| Linux | 支持 | Bubblewrap，依赖 `/usr/bin/bwrap`，内核需允许 User Namespace |
| Windows | 支持 | LPAC / AppContainer、目录 ACL、私有 Desktop 和 Job Object；实验性，待 Windows 实测 |

系统组件不可用时拒绝执行命令，不会退回普通进程执行。可通过 `errors.Is(err, sandboxfs.ErrShellUnavailable)` 判断安全 Shell 是否不可用；路径或操作被拒绝可检查 `sandboxfs.ErrDenied`，底层文件系统错误也可能直接返回。

### macOS / Linux

Shell 可读写工作区和每次运行的独立临时空间，并只读访问运行所需的系统目录。macOS 开放 `/usr`、`/bin`、`/System/Library`；Linux 开放存在的 `/usr`、`/bin`、`/lib`、`/lib64`。开启网络时额外提供必要的 DNS 和证书配置。

Linux 需自行安装 Bubblewrap。macOS 的 `sandbox-exec` 已被系统标记为弃用，部署时应在目标系统版本上验证兼容性。

### Windows

要求 Windows 10 1703 / Server 2019 或更新版本、支持持久 ACL 的本地磁盘，以及当前用户对工作区的 ACL 修改权限。Shell 不支持 UNC 或设备路径工作区。

实现直接通过 Go 调用 Win32 API，每次命令创建独立的 LPAC 身份，临时授予工作区访问权限，并使用私有 Desktop 和 Job Object 管理进程及其后代。命令结束后撤销授权并清理私有目录和身份配置；授权遍历最多 10000 项。

开启网络后，访问宿主机本地服务的回环连接仍受 Windows 系统限制。未授权的第三方工具目录及 PowerShell / .NET 所需的额外系统资源可能不可用。

Windows Shell 尚未完成目标平台运行验证；交叉编译通过不能代替隔离效果和兼容性测试。

## API

| 方法 | 功能 |
| --- | --- |
| `LsInfo` | 列出目录内容 |
| `Read` | 读取文本，支持行偏移和行数限制 |
| `Write` | 创建或覆盖文件，自动创建父目录 |
| `Edit` | 替换文件中的指定文本 |
| `GrepRaw` | 使用 Go 正则表达式搜索文件内容 |
| `GlobInfo` | 根据 Glob 模式查找文件和目录 |
| `Execute` | 同步执行沙箱命令 |
| `ExecuteStreaming` | 流式执行沙箱命令 |
| `Close` | 释放工作区目录句柄 |

当前不支持多模态读取、后台任务，以及 Grep 的多行模式和上下文选项。

## 使用限制

| 项目 | 限制 |
| --- | --- |
| 单文件读写 | 4 MiB |
| 单次文本读取 | 默认及最多 2000 行 |
| 单次目录列表 | 1024 项 |
| 搜索遍历 | 10000 项 |
| 搜索结果 | 最多 1000 条，Grep 命中内容合计最多 1 MiB |
| 单次命令输出 | 1 MiB，标准输出与标准错误合计 |

Shell 使用最小环境，HOME、临时文件及常用工具缓存位于每次运行的独立空间。工作区内的文件均视为可访问资源，包括 `.env` 等敏感文件，应由调用方选择合适的工作区。

路径守卫无法隔离工作区内已有的硬链接和挂载点。工作区及其父目录应由可信宿主管理，使用期间不要移动或替换；Shell、文件工具和外部编辑器的并发修改也需要调用方协调。

本组件不提供 CPU、内存、磁盘和进程数硬配额。macOS 无法保证清理主动脱离进程组的全部后代，但后代仍继承 Seatbelt 策略。Windows 宿主进程异常退出时，Job 会终止后代，但 ACL 和身份配置可能残留，目前没有跨重启回收机制。

## 示例与测试

以下命令在当前 `sandboxfs` 模块根目录执行；从仓库根目录开始时，先运行 `cd sandboxfs`。

- [命令执行示例](example/cmd/main.go)
- [Agent 集成示例](example/agent/main.go)：运行前修改工作区与提示词，并配置 `OPENAI_API_KEY`、`OPENAI_BASE_URL`、`OPENAI_MODEL_NAME`。

```bash
go run ./example/cmd -workspace /absolute/path/to/project -command 'echo hello'
go test -race ./...
go vet ./...
```

真实沙箱测试需要在对应操作系统上显式开启：

```bash
RUN_SANDBOX_SMOKE=1 go test -race -count=1 -timeout=120s -v . -run 'Test.*Smoke$'
```

Windows PowerShell 中先执行 `$env:RUN_SANDBOX_SMOKE='1'`，再运行上述 `go test` 命令。可选设置 `SANDBOXFS_WINDOWS_NETWORK_TARGET=host:port`，验证开启网络后的非回环 TCP 连接。运行环境需允许启动系统沙箱，外层沙箱可能阻止嵌套执行。

## 许可证

本项目采用 [Apache License 2.0](LICENSE)。模块内保留许可证副本，便于独立分发。
