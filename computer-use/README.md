# Eino Computer Use

面向 CloudWeGo Eino 的桌面工具，提供应用枚举、窗口截图、无障碍控件读取和语义操作。API 使用 `Config`、`Param`、`NewComputerUseTool`、`Execute`、`Cleanup`，主要接口、状态管理和平台适配均带中文注释。

macOS 使用纯 Go 后端，在 Go 进程内通过 `github.com/ebitengine/purego` 调用系统自带的 AppKit、Accessibility 和 CoreGraphics，支持 `CGO_ENABLED=0`。**无需外部控制程序、后台服务或相关环境变量。** `purego` 是由 Go Modules 自动获取的库依赖。

Windows 目前使用内嵌的 UI Automation PowerShell 脚本，尚未迁移成纯 Go。

## 支持的能力

| 部分 | 实现 |
| --- | --- |
| 模型工具 | `status`、`list_apps`、`start`、`get_app_state`、`click_element`、`type_text`、`press_key`、`scroll`、`perform_secondary_action`、`stop` |
| Host | Provider 选择、全局开关、主 Agent / 只读限制、图片输入模型检查、应用批准、租约复用 / 接管 |
| 状态 | 会话归属、最新快照绑定、元素元数据校验、租约 / 快照 TTL、动作后失效、释放图片 |
| macOS 默认 | 纯 Go 应用枚举 / 启动、窗口匹配、窗口截图、AX 树、元素缓存、语义操作和目标 PID 按键；没有 daemon |
| Windows | 内嵌 UIA 脚本、请求 / 结果文件、结果大小限制、元素实时比对、语义点击 / 输入 / 滚动 |
| Eino | 原生多模态 `EnhancedInvokableTool`，另提供旧式字符串 `InvokableTool` 适配器 |
| 宿主集成 | Ask / Auto / Operate 模式、应用审批回调、执行事件、共享 Host、上下文取消、幂等清理 |

宿主通过回调和 `Settings` 接入授权界面及偏好存储。macOS 后端没有动画光标；能力声明为 `NativeCursor: false`、`BackgroundControl: "partial"`，不同应用对后台事件的接受程度可能不同。

## 环境

- Go 1.25+；固定依赖 Eino `v0.9.22`、purego `v0.11.1`。
- macOS 默认后端：macOS 14+、可交互的桌面、辅助功能与屏幕录制权限；不需要 C 编译器、Swift 或 Node.js。
- Windows 真实控制：可交互的 Windows 桌面、Windows PowerShell 和 .NET UI Automation。
- Linux 不提供真实桌面 Provider，但显式选择 `mock` 可以运行完整示例与测试。

## 安装

本组件是 `eino-plugins` 仓库中的独立 Go module。公开版本发布后，在自己的 Go 项目中执行：

```sh
go get github.com/sda1-hacker/eino-plugins/computer-use
# 指定已发布版本，例如：
go get github.com/sda1-hacker/eino-plugins/computer-use@v0.1.0
```

导入路径与模块路径一致，包名为 `computeruse`。macOS 默认后端随 Go 程序编译，`go get` 获取的模块已经包含运行所需代码；系统权限仍由 macOS 管理。Windows UIA 脚本已经内嵌。

仓库标签使用 `computer-use/v0.1.0` 形式，具体流程见 [仓库发布说明](https://github.com/sda1-hacker/eino-plugins/blob/main/docs/releasing.md)。

## 先运行无需桌面的示例

以下所有构建、测试和示例命令均在当前 `computer-use` 模块根目录执行。若刚克隆 `eino-plugins`，先运行 `cd computer-use`：

```sh
go run ./example/local
```

示例通过真实 Eino `ToolsNode` 调用模拟桌面，演示建立租约、截图、输入中文、重新观察和停止，输出中会显示图片 part。不需要模型 API Key，也不触碰真实应用。直接 Go API 的用法见下文。

## 直接使用 Go API

```go
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
    settings.Enabled = true // 默认关闭，由宿主明确开启。

    desktop, err := computeruse.NewComputerUseTool(ctx, &computeruse.Config{
        Settings: &settings,
        Providers: []computeruse.Provider{computeruse.NewMockProvider()},
        DefaultProviderID: "mock",
        Model: &computeruse.ModelInfo{Input: []string{"text", "image"}},
    })
    if err != nil {
        return err
    }
    defer desktop.Cleanup()

    for _,param := range []*computeruse.Param{
        {Action: computeruse.ActionStart, AppID: "app.notes"},
        {Action: computeruse.ActionGetAppState},
        {Action: computeruse.ActionStop},
    } {
        result, err := desktop.ExecuteContext(ctx, param)
        if err != nil {
            return err
        }
        if result.Error != "" {
            return fmt.Errorf("%s: %s", result.ErrorCode, result.Error)
        }
        fmt.Println(result.Output)
    }
    return nil
}
```

`Execute(param)` 使用构造时的上下文，风格与 browseruse 一致；`ExecuteContext(ctx, param)` 支持每次请求独立的取消 / 超时。业务错误返回在 `ToolResult.Error / ErrorCode`，取消与超时通过 Go error 返回。

`Model.Input` 是宿主对模型能力的声明，工具不能自动探测模型服务。真实使用时，应确保所选模型和 Eino ChatModel adapter 都支持工具返回的图片。

## 接入 Eino

工具自身实现 `tool.EnhancedInvokableTool`，可直接放进 `ToolsNodeConfig.Tools`：

```go
node, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{
    Tools: []tool.BaseTool{desktop},
    ExecuteSequentially: true, // 桌面状态有先后依赖，关闭并行工具调用。
})
```

调用 `get_app_state` 时，返回一个文本 part 和一个图片 part，图片使用 `schema.ToolOutputImage.Base64Data` 与 `MIMEType`，没有把 Base64 当普通文字交给模型。元素摘要、leaseId、snapshotId 和平台能力保留在文本 JSON 里；`details.accessibilityTree` 保留 macOS 普通状态标签等没有操作编号的 AX 文本。

已有支持工具调用与图片输入的 ChatModel 时，可以使用 `example/agent.New` 创建 ReAct Agent，或直接配置：

```go
agent, err := react.NewAgent(ctx, &react.AgentConfig{
    ToolCallingModel: chatModel,
    MessageModifier: computeruse.ToolImagesAsUserMessages,
    ToolsConfig: compose.ToolsNodeConfig{
        Tools: []tool.BaseTool{desktop},
        ExecuteSequentially: true,
    },
    MaxStep: 40,
})
```

`example/agent` 接受应用已有的 ChatModel，不绑定模型厂商或额外引入某家服务的 API Key。它已配置 `ToolImagesAsUserMessages`：对只允许 tool 消息包含文本的聊天接口，把截图放入同批工具回复之后的 user 图片消息，保留工具调用 ID，避免把图片变成普通 Base64 文本。支持原生 tool 图片的接口也可以直接使用工具的多模态返回。调用结束后，使用独立清理上下文释放会话，或者在整个工具生命周期结束时调用 `Cleanup()`。

如果现有项目必须使用 browseruse 的字符串接口：

```go
legacy := desktop.AsTextTool() // 实现 tool.InvokableTool。
resultJSON, err := legacy.InvokableRun(ctx, `{"action":"get_app_state"}`)
```

这个适配器只返回 JSON。**使用字符串接口时，需要宿主自行将 `base64_image` 转为图片消息；推荐默认使用多模态接口。** Go 不支持同名方法重载，无法让同一个结构体同时实现两种 `InvokableRun` 签名，因此使用两个适配类型。

## macOS：直接使用纯 Go 后端

macOS 自动选择 `macos:ax`，无需设置 `Config.MacOS`。`MacOSConfig` 只包含平台和超时配置，不提供外部命令入口。例如读取指定应用：

```sh
CGO_ENABLED=0 go run ./example/desktop -app com.apple.TextEdit -request-permissions
```

示例只批准命令行中指定的 appId，并执行 start → get_app_state → stop。`-request-permissions` 显式请求辅助功能与屏幕录制权限；在系统设置中为实际运行程序、终端或 IDE 授权。

调用过程是 `Eino → computer 工具 → Host → macos:ax → macOS 系统 API`。应用和窗口通过 NSWorkspace / CGWindow 枚举；AX 读取控件并执行 AXPress / AXShowMenu；文本优先使用 AXSelectedText，不支持时回退到发往目标 PID 的 Unicode CGEvent。滚动使用聚焦后发送方向键，效果由应用决定。截图编码为 JPEG，以 Eino 图片 part 返回。

元素编号只对应当前快照，动作后需要重新观察。窗口通过公开 AX 坐标 / 尺寸匹配，重叠窗口优先按标题消歧；无法唯一确定时返回错误，不猜测目标。没有跨进程 helper 协议、socket 或后台服务。

## Windows：连接 UI Automation

不需要单独复制 `.ps1` 文件，Go 通过 `go:embed` 携带 helper，运行时写入私有临时目录。

```sh
go run ./example/desktop -provider windows:uia -app pid:1234
```

先使用 `list_apps` 得到当前应用 PID / 窗口。Windows 的 `start` 绑定已有窗口，不会自动安装或启动应用。`type_text` 和 `scroll` 必须带当前快照中的 `elementId`。

当前实现严格保留后台 UIA 策略：不提供通用按键、双击、拖拽和坐标点击。窗口截图沿用 `CopyFromScreen`，被遮挡 / 最小化的窗口可能无法获得正确图像；不能把 UIA 动作成功等同于截图可靠。

## 会话、审批和事件

每个工具有默认会话。Web 服务或多 Agent 应用应显式绑定稳定身份，并让同一桌面的工具共享一个 Host：

```go
host, err := computeruse.NewHost(config)
// 应用关闭时负责 host.Dispose(cleanupCtx)。
desktop, err := computeruse.NewComputerUseTool(ctx, &computeruse.Config{Host: host})
sessionCtx := computeruse.WithSession(ctx, computeruse.Session{
    ID: "chat-123",
    AgentID: "main",
    AccessMode: "operate",
})
result, err := desktop.ExecuteContext(sessionCtx, param)
```

会话身份、模型声明和授权结果只来自宿主代码，不在模型参数 schema 中。新租约的接管规则是：同一目标 / owner 复用，不同目标 / owner 会等待清理旧租约再开始。

应用许可通过以下配置接入：

- `Settings.AppApprovals`：宿主持久化的应用白名单；使用 `ApproveApp` / `RevokeApp` 动态调整。
- `PermissionAsk`：默认模式，调用 `ConfirmApp`；未提供回调时返回 `APP_APPROVAL_REQUIRED`。
- `PermissionAuto`：先调用 `ReviewApp`，返回 `ask_user` 时在会话允许询问且有回调的情况下调用 `ConfirmApp`。
- `PermissionOperate`：宿主已授权当前会话，跳过应用确认；仍遵守工具开关、模型能力和系统权限。

审批回调返回 `ApprovalDecision{Status:"approved", Scope:"session"}` 可以只批准当前租约；`Scope:"app"` 写入 Host 的内存白名单。需要跨重启保存时，由宿主维护偏好存储。

`OnEvent` 接收 running / done / error / clear，适合连接 UI；回调同步运行，不要在回调里重入同一个 Host。`RequestPermissions` 是宿主 API，不作为模型动作暴露。

## 运行约定

- 观察和动作使用可取消的串行队列；同桌面共享 Host 时不会并行替换元素缓存。
- 默认动作后使旧快照失效，防止重复使用陈旧元素。`PreserveSnapshotAfterAction: true` 可改为“直到下一次观察才过期”。
- 只保留最新截图，释放租约会删除图片；支持可选 `LeaseTTL` 和 `SnapshotTTL`。
- `Stop` / `Dispose` 可以取消同会话正在执行的动作，释放 Go 内持有的 AX / CF 对象；取消本身不能撤销已经发生的界面动作。
- 纯 Go 后端只使用公开系统 API；没有动画光标或 Chromium 专用的 AX observer 兼容逻辑，第三方应用需单独实测。
- 原生动作返回的文字警告会被保留，不再无条件退化成 `{ok:true}`。
- Windows 滚动使用 amount 指定次数。
- 新目标获批后才释放旧租约；撤销应用批准会终止对应活跃租约。
- 工具参数使用严格 JSON 解码，拒绝未知字段和伪造的快照元数据。

## 测试

```sh
CGO_ENABLED=0 go test ./...
go test -race ./...
go vet ./...
GOOS=windows GOARCH=amd64 go build ./...
GOOS=linux GOARCH=amd64 go build ./...
```

自动测试覆盖 Host 策略、会话隔离、快照版本、审批回调、并发与取消、默认后端选择、窗口消歧、按键取消配对、CF 对象清理、Windows 文件协议和 Eino 多模态调用链。测试使用 Mock / 函数替身，不操作真实桌面。race 检查使用 Go race 运行时，单独通过 `CGO_ENABLED=0` 的构建与实机验收确认生产后端不需要 Cgo。

本机 macOS 14.1 arm64 的真实 `Eino → 纯 Go Provider → 系统 API → AppKit 测试窗口` 回归已通过，覆盖全部 10 个工具动作，包括截图、输入框聚焦、AX 中文输入、中文键盘回退、回车 / 删除 / Escape、上下滚动、次级菜单与停止清理。验证报告记录 `backend: "macos:ax"`、`cgoEnabled: false`、`externalHelperUsed: false`。

真实 `mimo-v2.6-flash` + Eino ReAct 验收也已通过：从截图读取不在 AX 树中的随机视觉码，输入中文、点击按钮，再独立读回确认。过程中一次旧快照请求被正确拒绝，模型重新观察后恢复；完整报告保留该重试，不声称每个工具调用都成功。Windows 真实 GUI 和其他应用兼容性仍未实测。结果及截图见 [验证记录](docs/verification.md)。

授权后可重复执行：

```sh
node testdata/verify.mjs --request-permissions
```

这是开发验收脚本：Node.js 负责调度，系统 `swiftc` 仅编译独立的被测窗口，待验证的 Go 工具使用 `CGO_ENABLED=0`，不使用外部桌面驱动。**这些测试设施不是组件的构建 / 运行依赖。** 程序经真实 Eino `ToolsNode` 验证动作的实际效果并关闭测试窗口。报告保存在 `.cache/validation/native-verification.json`，只有全部断言与清理通过才标记 `passed: true`。默认模式不调用模型服务。

需要真实模型验收时，先在环境中配置 `OPENAI_API_KEY`、`OPENAI_BASE_URL`、`OPENAI_MODEL_NAME`，然后执行：

```sh
node testdata/verify.mjs --model
```

该模式会调用配置的模型服务，只向模型发送独立验证窗口的观察内容。随机视觉码不会放入提示词或 AX 文本；通过标准是模型读图后实际输入正确内容、点击一次、重新观察并停止。适配器在 `.cache/validation/model-runner` 的独立 Go module 中运行，不增加组件的模型厂商依赖。报告为 `.cache/validation/model-verification.json`，不会记录 API Key。

## 目录结构

与同仓库的 sandboxfs 一样，主包保持平铺，平台差异通过文件后缀隔离，用户示例统一放在 `example/`。建议先读 `computeruse.go` 和 `types.go`，需要了解会话控制时再读 `host.go`。

```text
computer-use/
├── computeruse.go       工具入口、动作分发、Eino 多模态适配与包说明
├── types.go             配置、参数、结果、Provider 接口和错误类型
├── host.go              授权、会话、租约、快照及注册表
├── provider.go          macOS Provider 外观、窗口匹配及模拟桌面
├── uia.go               Windows UIA 协议、数据解析及命令执行
├── platform_darwin.go   macOS 系统框架绑定、AX、截图与输入
├── platform_windows.go Windows 进程配置
├── platform_other.go   其他平台的进程配置与能力占位
├── *_test.go            按上述职责合并的测试
├── go.mod / go.sum      独立模块及依赖
├── example/
│   ├── local/           无 API Key 的 ToolsNode 示例
│   ├── desktop/         指定真实应用的状态读取示例
│   └── agent/           注入已有 ChatModel 的 ReAct 接入函数
├── testdata/
│   ├── verify.mjs       独立窗口开发验收脚本
│   ├── verification/    真实 Eino 工具验收程序
│   ├── macos-fixture/   独立 AppKit 测试窗口
│   └── model-runner/    可选的真实模型验收器模板
├── assets/              内嵌 Windows UIA 脚本
├── docs/                验证记录和实测证据
├── README.md
└── NOTICE
```

遵循仓库的 [Apache-2.0 许可证](../LICENSE)。已采用代码的版权与来源声明见 [NOTICE](NOTICE)。
