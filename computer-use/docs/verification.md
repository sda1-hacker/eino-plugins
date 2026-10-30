# macOS 纯 Go 后端与真实模型验证记录

验证日期：2026-10-30（Asia/Shanghai）。本文证据针对纯 Go 的 `macos:ax` 后端。

## 当前结论

本机 macOS 14.1 arm64 的纯 Go 原生回归与真实 MiMo / Eino 验收均通过。两种验收的 Go 程序都使用 `CGO_ENABLED=0`，没有启动外部桌面驱动或 socket 服务。

- 15:57 的原生回归：37 个调用 / 断言全部通过，覆盖公开的全部 10 个工具动作。
- 14:52 的真实模型验收：`mimo-v2.6-flash` 经 Eino ReAct 读取截图中的随机视觉码，输入中文、点击按钮、重新观察并停止。验证器独立核对了真实界面状态。
- 两次运行均确认独立测试窗口退出，报告包含 `backend: "macos:ax"`、`cgoEnabled: false`、`cleanup.externalHelperUsed: false`。

证据：[原生报告](verification-artifacts/macos-result.json)、[原生回读值](verification-artifacts/macos-observed-values.json)、[原生最终窗口](verification-artifacts/macos-final.jpeg)、[模型报告](verification-artifacts/model-result.json)、[模型最终观察](verification-artifacts/model-observed-values.json)、[模型最终截图](verification-artifacts/model-final.jpeg)。

## 运行依赖与实现

| 项目 | 实际值 |
| --- | --- |
| 操作系统 | macOS 14.1，Build 23B2073，arm64 |
| Go 编译器 | Go 1.27.1；模块最低 Go 版本为 1.25 |
| 默认 Provider | `macos:ax`，进程内调用系统框架 |
| Go 库 | Eino `v0.9.22`、purego `v0.11.1` |
| Cgo | 验收程序关闭，`CGO_ENABLED=0` |
| 模型适配器 | Eino-ext OpenAI 兼容适配器 `v0.1.13`，仅用于临时验证模块 |
| 系统权限 | 辅助功能、屏幕录制均已授权 |

`purego` 通过 Go Modules 获取，用于无 Cgo 的函数绑定。默认后端加载 macOS 自带的 CoreFoundation、ApplicationServices、CoreGraphics、ImageIO 和 AppKit，没有额外安装动态库。应用枚举使用 NSWorkspace，控件读取和动作使用公开 AX API，键盘回退使用目标 PID 的 CGEvent。窗口图像经过 Go JPEG 编码，以 Eino 图片 part 返回。

Node.js 只用于开发验收调度；系统 Swift 5.9.2 只编译被操作的独立 AppKit 测试窗口。它们不是 Go 组件的构建或运行依赖。Go 工具直接操作系统，不调用外部桌面驱动。

## 原生回归

真实 Eino ToolsNode 控制临时应用 `local.eino.computeruse.validation`，不会操作用户已有文档。

| 工具动作 | 真实效果验证 | 结果 |
| --- | --- | --- |
| status | 可读取 Provider 和系统权限状态 | 通过 |
| list_apps | 能发现独立测试应用 | 通过 |
| start | 绑定目标应用及窗口 | 通过 |
| get_app_state | 指定窗口截图可解码，以 Eino 图片 part 返回 | 通过 |
| click_element | 输入框聚焦；按钮标题变为已点击 1 次 | 通过 |
| type_text | AX 中文输入、自绘控件 CGEvent 中文回退均可读回 | 通过 |
| press_key | Return 触发确认；Delete 删除一个汉字；Escape 关闭真实菜单 | 通过 |
| scroll | NSTextView 向下偏移增加，向上偏移减少 | 通过 |
| perform_secondary_action | AXShowMenu 打开真实 NSMenu，状态显示已打开 | 通过 |
| stop | 会话停止，释放元素缓存和 CF 对象 | 通过 |

动作返回成功只表示系统调用成功；验证器轮询后续 AX 状态，核对文字或实际视口偏移。37 步包含动作、观察和断言，轮询次数可能随机器响应速度变化。

新增自动测试还覆盖：平台检查、操作取消与超时、重叠窗口消歧、取消时仍成对发送 keyUp、重复清理不重复释放 CF 对象。

## 真实模型验收

配置通过 `os.Getenv` 读取 `OPENAI_API_KEY`、`OPENAI_BASE_URL`、`OPENAI_MODEL_NAME`；密钥和 endpoint 不写入报告。

黄色画布上的随机六位视觉码不提供 AX 文本。验证器持有期望值，但不把它放入提示词。模型只能通过 computer 工具观察指定窗口，不能读取环境变量、文件或枚举其他应用。

本轮视觉码为 `244311`，模型实际输入 `模型验收：244311`，按钮最后为“已点击 1 次”。验收断言满足：真实调用模型、收到截图、首次 AX 文本不含视觉码、输入正确且可回读、按钮效果可回读、模型 stop、资源清理成功。

14:52 的模型验收共 11 次模型请求、10 次工具调用、4 次截图。本轮清理后再次编译并自检了模型验收器，未重复调用付费模型服务。模型点击输入框后直接输入，旧快照请求被 Host 拒绝；它重新观察后恢复。报告保留这次拒绝，`all_tool_calls_succeeded` 为 false；通过的是完整任务与恢复验收。

## 目录整理回归

主包根目录从 26 个 Go 文件合并为 13 个：实现文件由 18 个减少到 8 个，测试按职责合并。入口与多模态适配集中于 `computeruse.go`，公共类型集中于 `types.go`，Host 与状态注册表合并于 `host.go`，平台实现按 `platform_*.go` 隔离。示例统一为 `example/`，开发验收收进 `testdata/`。

整理前后主包 `go doc -all` 输出完全一致。新路径的本地 ToolsNode 示例返回两次图片观察，ReAct 回归与全部 52 个测试 / 子测试通过；15:57 的真实窗口验收再次通过 37 步。没有新增模型服务调用。

## 自动化检查

- `CGO_ENABLED=0 go test ./...`：通过。
- Go race：52 个测试 / 子测试通过，0 失败。
- `go vet ./...`、`go mod tidy -diff`：通过。
- macOS amd64、Windows amd64、Linux amd64 的 `CGO_ENABLED=0` 交叉编译：通过，仅证明可构建。

## 重复运行

仅运行组件与非桌面测试需要 Go；默认 macOS 后端无需任何 helper 配置：

```sh
CGO_ENABLED=0 go test ./...
CGO_ENABLED=0 go run ./example/desktop -app com.apple.TextEdit -request-permissions
```

完整开发验收脚本需要 Node.js 和系统 swiftc 创建独立测试应用，测试中的 Go 后端仍关闭 Cgo：

```sh
node testdata/verify.mjs
node testdata/verify.mjs --model
```

第二条命令需上述三个模型环境变量，会调用配置的模型服务。模型适配器在 `.cache/validation/model-runner` 独立模块中构建，首次可能下载公开 Go 依赖，不改变组件的 go.mod。验收脚本不接受外部驱动参数。

报告和全部观察位于 `.cache/validation/`；核心证据另存于本文链接的 verification-artifacts。最新原生报告仅记录测试窗口清理结果；14:52 的模型历史报告仍带有 `daemonStopped: true` 字段，表示当时没有自有后台服务，需结合 `externalHelperUsed: false` 解读。

## 未覆盖与差异

- 本轮控制的是已运行的测试应用；没有验证所有应用的冷启动路径。
- Windows 仍使用 PowerShell UIA，尚未迁移为纯 Go，也未进行 Windows 实机验证。Linux 仍只有 mock。
- 纯 Go 后端没有动画光标或 Chromium 专用的 AX observer 兼容逻辑。能力声明为部分后台控制，不同应用的后台输入行为需要单独验收。
- 截图使用公开 CGWindow API；其他 macOS 版本、受保护窗口、其他模型和第三方应用需要单独验收。
- AX 窗口通过公开几何属性和标题匹配，无法唯一确定目标时返回错误。树遍历深度最多 8、总节点最多 350、可操作节点最多 80，复杂界面可能被截断。
- 按键实测为 Return / Delete / Escape，上下滚动使用方向键，效果取决于应用。
