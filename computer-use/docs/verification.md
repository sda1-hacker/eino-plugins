# 验证记录

验证日期：2026-10-30（Asia/Shanghai）。

## 当前结论

**macOS 核心端到端验证通过。** 用户开启无障碍权限后，经真实 Eino `ToolsNode`、Go Provider 和编译后的原生 helper，完成了独立 AppKit 窗口的截图、中文输入与回读、按钮点击与回读、停止和进程清理。

本次成功运行时间为 2026-10-30 10:06（Asia/Shanghai），11 个步骤 / 断言全部通过。范围包括 `list_apps`、`start`、`get_app_state`、`type_text`、`click_element`、`stop`；Windows 桌面、真实模型服务推理、其他原生动作和其他应用兼容性没有在本场景中实测。

保留的证据：[机器报告](verification-artifacts/macos-result.json)、[实际 AX 回读值](verification-artifacts/macos-observed-values.json)、[最终窗口截图](verification-artifacts/macos-final.jpeg)。

## 环境与原生构建

| 项目 | 实际值 |
| --- | --- |
| 操作系统 | macOS 14.1，Build 23B2073 |
| CPU | arm64 |
| 系统 Swift | Apple Swift 5.9.2 |
| 验证用 Swift | 官方 Swift 6.2.4，下载后在临时目录解包使用 |
| SDK | Command Line Tools 的 macOS 14.2 SDK |
| Cua 依赖 | `d38bfbfb6b1d4296903477f517b1a0fa54af497b` |
| helper 自报版本 | `0.1.0` |
| 构建结果 | `dist-computer-use/mac-arm64/hana-computer-use-helper`，约 11 MiB |

Swift 安装包签名已通过系统校验：`Developer ID Installer: Swift Open Source (V9AUD2URP3)`，Apple notarization trusted。没有调用安装器、修改系统 Swift 或切换系统 Xcode。

锁定的 `swift-log 1.12.0` 和 `swift-collections 1.4.1` 要求 Swift 6.2，因此已将构建预检从 6.0 修正为 6.2。构建前后 `Package.resolved` 内容一致。

实际编译暴露并修复了 macOS 14 SDK 的并发标注兼容问题：

- 两个 SwiftUI 视图显式隔离到 MainActor。
- 旧 SDK 中尚未标注 Sendable 的 Logger 使用兼容导入。
- 延迟恢复焦点及应用启动流程跨 actor 传递 PID，在目标上下文重新查找应用，避免跨 actor 返回 NSRunningApplication。

这些修复仅在旧 SDK 构建时应用；没有将整个项目降到 Swift 5 语言模式，也没有关闭 Swift 6 并发检查。

## 已执行的真实调用

验证程序使用自己的临时应用 `local.eino.computeruse.validation`，通过真实 Eino `ToolsNode` 调用编译后的 helper。

| 步骤 | 结果 |
| --- | --- |
| 启动独立 AppKit 测试窗口 | 通过 |
| `list_apps` 枚举真实应用 | 通过 |
| `start` 绑定测试应用和窗口 | 通过 |
| `get_app_state` 获取 AX 树与截图 | 通过 |
| 解码原生截图及确认 Eino 图片 part | 通过，JPEG，1280 × 616 |
| 中文输入并从下一次观察读回 | 通过：`Eino 真实桌面验证：中文输入成功` |
| 点击按钮并读取标题变化 | 通过：`Eino 验证按钮，已点击 1 次` |
| `stop` 与进程清理 | 通过，无残留 helper / 测试窗口进程 |

第一次验证因权限不足返回 `Accessibility permission not granted.`，没有计作通过。用户授权后的检查显示 Accessibility 和 Screen Recording 均为 granted。

授权后的首次复验还发现 Go Markdown 解析器丢失了括号中的 `AXDescription`，并且未提取 `AXValue`。已补齐标签、值与标识符解析，添加回归测试；后续真实运行完整通过。人工查看最终截图，也确认输入文字与按钮状态可见。

详细机器结果与构建日志保存在 `.cache/validation/`，其中 `native-verification.json` 当前为 `passed: true, blocked: false`。关键结果和截图另存于 `docs/verification-artifacts/`，不会随构建缓存清理丢失。

## 自动化验证

- `go test -race -count=1`：Go 工具与 Eino ReAct 自动测试通过；包含新增的真实 Cua Markdown 格式回归测试，0 个失败事件。
- `go vet ./...`：通过。
- `node --test scripts/build-computer-use-helper.test.mjs`：4 个构建预检 / SDK 补丁测试通过。
- 原生生产构建：CuaDriverCore、CuaDriverServer、HanaComputerUseHelper 编译与链接通过。
- 上一轮 Windows / Linux / macOS Intel 的 Go 交叉编译通过；不表示 Windows 原生 UIA 已在 Windows 桌面执行。

## 重复验证

确认 macOS 系统设置中的无障碍与屏幕录制授权仍有效，然后执行：

```sh
node scripts/verify-native.mjs "$PWD/dist-computer-use/mac-arm64/hana-computer-use-helper" --request-permissions
```

测试会重新创建独立窗口并覆盖机器报告。通过标准是：原生截图可解码且经 Eino 图片 part 返回，中文内容可以从下一次 AX 状态读回，按钮点击导致可观察的标题变化，最后成功停止并清理资源。
