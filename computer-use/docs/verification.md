# macOS 与真实模型验证记录

验证日期：2026-10-30（Asia/Shanghai）。

## 当前结论

本机 macOS 14.1 arm64 的原生回归与真实模型验收均已通过。

- 13:35 的原生回归：38 个调用 / 断言全部通过，覆盖公开的全部 10 个工具动作。
- 13:37 的真实模型验收：`mimo-v2.6-flash` 经 Eino ReAct 读取截图中的随机视觉码，输入中文、点击按钮、重新观察并停止。验证器独立核对了最后的真实界面状态。
- 两次运行均确认测试窗口退出、自有 helper daemon 停止，不修改用户已有文档。

证据：[原生报告](verification-artifacts/macos-result.json)、[原生回读值](verification-artifacts/macos-observed-values.json)、[原生最终窗口](verification-artifacts/macos-final.jpeg)、[模型报告](verification-artifacts/model-result.json)、[模型最终观察](verification-artifacts/model-observed-values.json)、[模型最终截图](verification-artifacts/model-final.jpeg)。

结论针对本机和本次场景，不代表所有 macOS 版本、第三方应用或所有按键组合均已验证。Windows 真实 UIA 尚未实测。

## 环境与构建

| 项目 | 实际值 |
| --- | --- |
| 操作系统 | macOS 14.1，Build 23B2073，arm64 |
| 系统 Swift | Apple Swift 5.9.2 |
| 构建 Swift | 官方 Swift 6.2.4，在临时目录解包使用 |
| SDK | Command Line Tools 的 macOS 14.2 SDK |
| Cua 依赖 | `d38bfbfb6b1d4296903477f517b1a0fa54af497b` |
| helper 自报版本 | `0.1.0`，来自上游，不能据此判断是否包含修复 |
| 模型适配器 | Eino-ext OpenAI 兼容适配器 `v0.1.13`，仅用于临时验证模块 |

Swift 安装包之前已通过签名及 Apple notarization 校验，没有安装或切换系统工具链。锁定的 swift-log 和 swift-collections 要求 Swift 6.2，因此构建检查实际传递依赖的下限。

本轮构建前后 `Package.resolved` 的 SHA-256 一致，没有升级 Swift 依赖。原有的 AX 深度 / 数量 / 超时补丁、AXShowDefaultUI、旧 SDK 并发标注和 PID 跨 actor 补丁均保留。动作映射对照 [OpenHanako macOS Provider](https://github.com/liliMozi/openhanako/blob/main/core/computer-use/providers/macos-cua-provider.ts)，继续禁用坐标点击、双击和拖拽。

## 本轮修复

1. **按键崩溃**：macOS 14.1 存在私有类，却缺少 `messageWithEventRecord:pid:version:` 方法。原实现直接发消息，导致 helper SIGABRT。现在先检查类方法，不支持时走上游已有的公开 `CGEvent.postToPid` 回退；所有 SDK 构建均应用此运行时检查。
2. **输入成功的假象**：部分自绘控件对不支持的 AXSelectedText 写入返回 success。现在先检查属性可写性，不支持时启用既有键盘回退，实测中文可正确读回。
3. **输入框点击**：没有声明 AXPress 的文本控件改用 AXFocused，并确认焦点已设置；不调用可能提交表单的 AXConfirm，也不使用坐标点击。
4. **状态标签丢失**：新增 `Snapshot.AccessibilityTree` 和 `details.accessibilityTree`，保留没有操作编号的普通 AX 文本，不为静态标签伪造元素编号。
5. **图片消息兼容**：新增 `ToolImagesAsUserMessages`。对只允许 tool 消息包含文本的聊天接口，把截图放到同批工具回复之后的 user 图片消息中，保留工具调用 ID 和文本。

补丁有幂等和源码锚点检查，上游形状不匹配时明确失败。没有修改系统设置或关闭 Swift 并发检查。

## 原生回归

真实 Eino ToolsNode 控制独立 AppKit 应用 `local.eino.computeruse.validation`。

| 工具动作 | 真实效果验证 | 结果 |
| --- | --- | --- |
| status | 可读取 Provider 状态 | 通过 |
| list_apps | 能发现独立测试应用 | 通过 |
| start | 绑定目标应用及窗口 | 通过 |
| get_app_state | 截图可解码，以 Eino 图片 part 返回 | 通过 |
| click_element | 输入框聚焦；按钮标题变为已点击 1 次 | 通过 |
| type_text | AX 中文输入、自绘控件 CGEvent 中文回退均可读回 | 通过 |
| press_key | Return 触发确认；Delete 删除一个汉字；Escape 关闭真实菜单 | 通过 |
| scroll | NSTextView 向下滚动偏移增加，向上偏移减少 | 通过 |
| perform_secondary_action | AXShowMenu 打开真实 NSMenu，状态显示已打开 | 通过 |
| stop | 会话停止，daemon 与测试窗口清理成功 | 通过 |

动作返回 success 不等于效果通过。验证器轮询后续 AX 状态并核对文本或视口偏移。38 步包含动作、观察和断言，轮询次数可能随机器响应速度变化。

10:06 的早期 11 步测试只覆盖截图、AX 输入、按钮和停止；后来发现的按键崩溃不在早期覆盖范围，本轮补齐了该缺口。

## 真实模型验收

使用环境变量 `OPENAI_API_KEY`、`OPENAI_BASE_URL`、`OPENAI_MODEL_NAME`，密钥和 endpoint 不写入报告。

黄色画布绘制随机六位视觉码，不提供 AX 文本；验证器持有期望值，但不会把它加入模型提示词。模型只有指定测试应用的 computer 工具，不能读取环境变量、文件或枚举其他应用。

验收断言全部满足：真实调用模型、收到截图、首次 AX 文本不含视觉码、最后输入为“模型验收：”加正确视觉码、按钮最后为已点击 1 次、模型 stop、资源清理成功。

本轮有 11 次模型请求、10 次工具调用、4 次截图观察。模型点击输入框后直接输入，旧快照请求被 Host 正确拒绝；它重新观察后恢复，最终物理结果通过。报告保留这次拒绝，`all_tool_calls_succeeded` 为 false，不能宣称所有模型调用均无重试。通过的是完整任务与安全恢复验收。

## 自动化检查

- Go race：52 个测试 / 子测试通过，0 失败。
- `go vet ./...`、`go mod tidy -diff`：通过。
- Node 构建补丁测试：7 项通过。
- 直接调用和 Eino ToolsNode 示例：通过。
- Windows / Linux amd64 交叉编译：通过，仅证明可构建。
- helper release 构建：通过，Swift 锁文件未变。

## 重复运行

在 computer-use 模块目录执行。配置好 Swift 6.2+ 后先重新构建，避免沿用旧 helper：

```sh
node scripts/build-computer-use-helper.mjs
node scripts/verify-native.mjs
```

如需请求系统权限，增加 `--request-permissions`。配置上述三个模型环境变量后，可执行：

```sh
node scripts/verify-native.mjs --model
```

模型适配器在 `.cache/validation/model-runner` 独立模块中构建，首次可能下载公开 Go 依赖，不改变组件的 go.mod。报告和观察保存在 `.cache/validation/`；核心证据另存于本文链接的 verification-artifacts。

## 未覆盖范围

Windows 实机、其他 macOS 版本、其他模型服务和第三方应用的全部控件兼容性未覆盖。按键实测为 Return / Delete / Escape，滚动实测为上下方向；Linux 仍仅有 mock Provider。
