# Eino Computer Use

将 [OpenHanako 的 computer-use](https://github.com/liliMozi/openhanako/tree/9a6cabf8c513db5edc6cf8179a538c9075885c6e/core/computer-use) 移植成 Go / Eino 工具。API 参考 Eino-ext 的 `browseruse`：`Config`、`Param`、`NewComputerUseTool`、`Execute`、`Cleanup`。主要接口、状态管理和平台适配均带中文注释。

Go 控制层已经实现；macOS 保留原版 Swift / Cua helper，Windows 保留原版 UI Automation PowerShell helper，无需 Node.js 或 TypeScript 服务参与运行。Node.js 只用于构建 macOS helper 和应用原来的上游补丁。

## 已迁移的能力

| 部分 | 实现 |
| --- | --- |
| 模型工具 | `status`、`list_apps`、`start`、`get_app_state`、`click_element`、`type_text`、`press_key`、`scroll`、`perform_secondary_action`、`stop` |
| Host | Provider 选择、全局开关、主 Agent / 只读限制、图片输入模型检查、应用批准、租约复用 / 接管 |
| 状态 | 会话归属、最新快照绑定、元素元数据校验、租约 / 快照 TTL、动作后失效、释放图片 |
| macOS | helper 查找、已有应用复用 / 启动、窗口选择、常驻 daemon、图片 / AX 树归一化、语义动作、原生光标 |
| Windows | 内嵌 UIA 脚本、请求 / 结果文件、结果大小限制、元素实时比对、语义点击 / 输入 / 滚动 |
| Eino | 原生多模态 `EnhancedInvokableTool`，另提供旧式字符串 `InvokableTool` 适配器 |
| 宿主集成 | Ask / Auto / Operate 模式、应用审批回调、执行事件、共享 Host、上下文取消、幂等清理 |

原项目的 Electron 确认面板、overlay 页面和应用偏好存储属于产品 UI，这里以回调和 `Settings` 替代。原生光标、Swift 源码、锁定依赖和构建补丁都包含在项目中。

## 环境

- Go 1.23+；固定依赖 Eino `v0.9.22`。
- macOS 真实控制：macOS 14+、Swift 6.2+、构建时需要 Node.js 18+ 和访问 SwiftPM 依赖仓库的网络；也可以配置已有的兼容 helper。
- Windows 真实控制：可交互的 Windows 桌面、Windows PowerShell 和 .NET UI Automation。
- Linux 不提供真实桌面 Provider，但显式选择 `mock` 可以运行完整示例与测试。

## 安装

本组件是 `eino-plugins` 仓库中的独立 Go module。公开版本发布后，在自己的 Go 项目中执行：

```sh
go get github.com/sda1-hacker/eino-plugins/computer-use
# 指定已发布版本，例如：
go get github.com/sda1-hacker/eino-plugins/computer-use@v0.1.0
```

导入路径与模块路径一致，包名为 `computeruse`。macOS 原生 helper 需要另外构建或配置；`go get` 只获取 Go 模块，不会执行 Swift 构建。Windows UIA 脚本已经内嵌。

仓库标签使用 `computer-use/v0.1.0` 形式，具体流程见 [仓库发布说明](https://github.com/sda1-hacker/eino-plugins/blob/main/docs/releasing.md)。

## 先运行无需桌面的示例

以下所有构建、测试和示例命令均在当前 `computer-use` 模块根目录执行。若刚克隆 `eino-plugins`，先运行 `cd computer-use`：

```sh
go run ./examples/basic
go run ./examples/eino
```

第一个演示建立租约、读图、输入中文、读回验证、停止；第二个通过真实 Eino `ToolsNode` 调用，输出中会显示图片 part。二者不需要模型 API Key，也不触碰真实应用。

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

调用 `get_app_state` 时，返回一个文本 part 和一个图片 part，图片使用 `schema.ToolOutputImage.Base64Data` 与 `MIMEType`，没有把 Base64 当普通文字交给模型。元素摘要、leaseId、snapshotId 和平台能力保留在文本 JSON 里。

已有支持工具调用与图片输入的 ChatModel 时，可以使用 `examples/agent.New` 创建 ReAct Agent，或直接配置：

```go
agent, err := react.NewAgent(ctx, &react.AgentConfig{
    ToolCallingModel: chatModel,
    ToolsConfig: compose.ToolsNodeConfig{
        Tools: []tool.BaseTool{desktop},
        ExecuteSequentially: true,
    },
    MaxStep: 40,
})
```

`examples/agent` 接受应用已有的 ChatModel，不绑定模型厂商或额外引入某家服务的 API Key。调用结束后，使用独立清理上下文释放会话，或者在整个工具生命周期结束时调用 `Cleanup()`。

如果现有项目必须使用 browseruse 的字符串接口：

```go
legacy := desktop.AsTextTool() // 实现 tool.InvokableTool。
resultJSON, err := legacy.InvokableRun(ctx, `{"action":"get_app_state"}`)
```

这个适配器只返回 JSON。**使用字符串接口时，需要宿主自行将 `base64_image` 转为图片消息；推荐默认使用多模态接口。** Go 不支持同名方法重载，无法让同一个结构体同时实现两种 `InvokableRun` 签名，因此使用两个适配类型。

## macOS：构建并连接原生 helper

```sh
node scripts/build-computer-use-helper.mjs
```

输出位于 `dist-computer-use/mac-arm64/hana-computer-use-helper`，Intel 使用 `mac-x64`。脚本会先校验 Swift 版本，再解析锁定依赖、应用 OpenHanako 的 AX 补丁并编译。可以用 `HANA_COMPUTER_USE_SWIFT_PATH` 指定 Swift 6.2+ 可执行文件。

之后读取一个指定应用：

```sh
go run ./examples/desktop -app com.apple.TextEdit \
  -helper "$PWD/dist-computer-use/mac-arm64/hana-computer-use-helper" \
  -request-permissions
```

示例只批准命令行中指定的 appId，并执行 start → get_app_state → stop。macOS 仍要求系统无障碍和屏幕录制权限；`-request-permissions` 显式触发权限请求。

程序中可通过 `MacOSConfig.Command` 或原来的 `HANA_COMPUTER_USE_HELPER_PATH` 指定 helper。还兼容 `HANA_COMPUTER_USE_RUNTIME_ROOT`、打包应用资源目录、开发构建目录和外部 `HANA_CUA_DRIVER_PATH`。

新实例默认创建独立 socket 路径，避免不同 Host 共用元素缓存。若配置已有的外部 `cua-driver` 和 socket，默认不会自动启动或关闭它的 daemon；需要自行启动服务。自带 helper 默认由 Provider 自动启停，只清理自己启动的进程。

原版锁定的 Swift 驱动需要同时提供截图和 AX 树，即 `capture_mode=som`；若用户配置为 `ax`，没有图片会明确报错。不要直接用新版本 Cua 的 token 协议替换这个锁定版本的 element index 协议。

## Windows：连接 UI Automation

不需要单独复制 `.ps1` 文件，Go 通过 `go:embed` 携带 helper，运行时写入私有临时目录。

```sh
go run ./examples/desktop -provider windows:uia -app pid:1234
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

会话身份、模型声明和授权结果只来自宿主代码，不在模型参数 schema 中。原版的新租约接管语义保留：同一目标 / owner 复用，不同目标 / owner 会等待清理旧租约再开始。

应用许可通过以下配置接入：

- `Settings.AppApprovals`：宿主持久化的应用白名单；使用 `ApproveApp` / `RevokeApp` 动态调整。
- `PermissionAsk`：默认模式，调用 `ConfirmApp`；未提供回调时返回 `APP_APPROVAL_REQUIRED`。
- `PermissionAuto`：先调用 `ReviewApp`，返回 `ask_user` 时在会话允许询问且有回调的情况下调用 `ConfirmApp`。
- `PermissionOperate`：宿主已授权当前会话，跳过应用确认；仍遵守工具开关、模型能力和系统权限。

审批回调返回 `ApprovalDecision{Status:"approved", Scope:"session"}` 可以只批准当前租约；`Scope:"app"` 写入 Host 的内存白名单。需要跨重启保存时，由宿主维护偏好存储。

`OnEvent` 接收 running / done / error / clear，适合连接 UI；回调同步运行，不要在回调里重入同一个 Host。`RequestPermissions` 是宿主 API，不作为模型动作暴露。

## 与上游的明确差异

- 观察和动作使用可取消的串行队列；同桌面共享 Host 时不会并行替换元素缓存。
- 默认动作后使旧快照失效，防止重复使用陈旧元素。`PreserveSnapshotAfterAction: true` 可保留上游“直到下一次观察才过期”的行为。
- 只保留最新截图，释放租约会删除图片；支持可选 `LeaseTTL` 和 `SnapshotTTL`。
- `Stop` / `Dispose` 可以取消同会话正在执行的动作，并尽力清理自有 daemon；取消本身不能撤销已经发生的界面动作。
- 原生请求取消或超时后重置自有 macOS daemon，避免远端动作继续影响下一次请求；外部 daemon 的生命周期仍由宿主管理。
- 原生动作返回的文字警告会被保留，不再无条件退化成 `{ok:true}`。
- Windows 滚动实际使用 amount；原脚本计算滚动次数但未使用。
- 新目标获批后才释放旧租约；撤销应用批准会终止对应活跃租约。
- 工具参数使用严格 JSON 解码，拒绝未知字段和伪造的快照元数据。

## 测试

```sh
go test -race ./...
go vet ./...
GOOS=windows GOARCH=amd64 go build ./...
GOOS=linux GOARCH=amd64 go build ./...
```

自动测试覆盖 Host 策略、会话隔离、快照版本、审批回调、并发与取消、原生 helper 协议、文件清理、Windows 结果大小限制和 Eino 多模态调用链。测试使用 Mock / fake runner，不会操作真实桌面。

本机系统自带 Swift 5.9。本次验证从 Swift 官方下载并验证签名，在临时目录使用 Swift 6.2.4，已成功编译原生 helper，没有替换系统工具链。macOS 14.2 SDK 所需的并发标注兼容补丁也已纳入构建脚本；构建强制使用 `Package.resolved`，不会自动升级锁定依赖。

授权后，真实 Eino → Go Provider → 原生 helper → AppKit 窗口的核心调用链已通过：11 个步骤 / 断言全部成功，包括实际截图解码、中文输入回读、按钮点击回读和停止；确认没有残留测试窗口或 helper 进程。实测还修复了 Cua Markdown 中括号标签和文本值的解析。完整结果及截图见 [验证记录](docs/verification.md)。Windows 真实 GUI、真实模型服务推理以及未列入该场景的其他原生动作尚未实测。

授权后可重复执行：

```sh
node scripts/verify-native.mjs "$PWD/dist-computer-use/mac-arm64/hana-computer-use-helper" --request-permissions
```

程序会创建独立 AppKit 测试窗口，经真实 Eino `ToolsNode` 执行截图解码、中文输入与回读、按钮点击与回读，最后清理测试窗口和自有 daemon。报告保存在 `.cache/validation/native-verification.json`，只有全部断言通过才标记 `passed: true`。它不调用模型服务，也不修改用户现有文档。

## 目录和来源

```text
*.go                Go 工具、Host、租约、Provider、中文注释及测试
doc.go              pkg.go.dev 中文包说明
go.mod / go.sum     当前组件的独立模块与依赖
assets/             内嵌 Windows UIA 脚本、原始光标资源
desktop/native/     原版 Swift helper 和锁定的 SwiftPM 依赖
scripts/            原版 Cua 构建补丁及环境预检
examples/basic/     无桌面的直接调用示例
examples/eino/      无 API Key 的 ToolsNode 示例
examples/agent/     注入已有 ChatModel 的 ReAct 接入函数
examples/desktop/   指定真实应用的状态读取示例
examples/verify/    真实 Eino + 原生 helper 的验证程序
testdata/macos-fixture/   独立 AppKit 测试窗口
docs/               上游代码分析
```

遵循 Apache-2.0，原始作者与修改说明见 [NOTICE](NOTICE) 和 [LICENSE](LICENSE)。
