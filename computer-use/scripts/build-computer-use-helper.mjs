// 源自 OpenHanako（Apache-2.0）。保留原生构建补丁，增加中文错误及版本/架构检查。
import fs from "fs";
import path from "path";
import { execFileSync } from "child_process";
import { fileURLToPath, pathToFileURL } from "url";

const __filename = fileURLToPath(import.meta.url);
const __dirname = path.dirname(__filename);

export function shouldBuildComputerUseHelper({ platform = process.platform } = {}) {
  return platform === "darwin";
}

export function swiftArchForNodeArch(arch = process.arch) {
  if (arch === "x64") return "x86_64";
  if (arch === "arm64" || arch === "x86_64") return arch;
  throw new Error(`不支持的 macOS 架构：${arch}`);
}

export function resolveComputerUseHelperBuildArch({
  argv = process.argv,
  env = process.env,
  arch = process.arch,
} = {}) {
  const explicitArg = Array.isArray(argv) ? argv[2] : null;
  return explicitArg || env.HANA_COMPUTER_USE_HELPER_ARCH || arch;
}

export function computerUseHelperOutputDir({
  rootDir = path.resolve(__dirname, ".."),
  osName = "mac",
  arch = process.arch,
} = {}) {
  return path.join(rootDir, "dist-computer-use", `${osName}-${arch}`);
}

export function swiftBuildScratchPath({
  rootDir = path.resolve(__dirname, ".."),
  arch = process.arch,
} = {}) {
  return path.join(rootDir, ".cache", "computer-use-helper", "swift-build", `mac-${arch}`);
}

const CUA_APP_STATE_RELATIVE_PATH = path.join(
  "checkouts",
  "cua",
  "libs",
  "cua-driver",
  "Sources",
  "CuaDriverCore",
  "AppState",
  "AppState.swift",
);
const CUA_CLICK_TOOL_RELATIVE_PATH = path.join(
  "checkouts",
  "cua",
  "libs",
  "cua-driver",
  "Sources",
  "CuaDriverServer",
  "Tools",
  "ClickTool.swift",
);
const CUA_EVENT_POST_RELATIVE_PATH = path.join(
  "checkouts", "cua", "libs", "cua-driver", "Sources",
  "CuaDriverCore", "Input", "SkyLightEventPost.swift",
);

const CUA_AX_PATCH_SENTINEL = "cuaDriverAXMessagingTimeoutSeconds";
const CUA_CLICK_PATCH_SENTINEL = '"show_default_ui": "AXShowDefaultUI"';

function replaceRequired(source, needle, replacement, label) {
  if (!source.includes(needle)) {
    throw new Error(`[computer-use-helper] Cua patch anchor not found: ${label}`);
  }
  return source.replace(needle, replacement);
}

export function patchCuaDriverAppStateSource(source) {
  if (source.includes(CUA_AX_PATCH_SENTINEL)) return source;
  let patched = source;

  patched = replaceRequired(
    patched,
    "extension AXObserver: @retroactive @unchecked Sendable {}\n\n/// No-op callback",
    `extension AXObserver: @retroactive @unchecked Sendable {}

private let cuaDriverAXMessagingTimeoutSeconds: Float = 0.35

@discardableResult
private func applyCuaDriverAXMessagingTimeout(_ element: AXUIElement) -> AXError {
    AXUIElementSetMessagingTimeout(element, cuaDriverAXMessagingTimeoutSeconds)
}

/// No-op callback`,
    "AX messaging timeout helpers",
  );

  patched = replaceRequired(
    patched,
    "    public static let maxDepth = 25\n",
    "    public static let maxDepth = 6\n    public static let maxActionableElements = 48\n",
    "bounded AX tree walk",
  );

  patched = replaceRequired(
    patched,
    `        let root = AXUIElementCreateApplication(pid)

        // Cue Chromium/Electron apps to turn on their web accessibility tree.
        // Non-Chromium apps ignore these attribute writes — safe no-op.
        try await activateAccessibilityIfNeeded(pid: pid, root: root)
`,
    `        let root = AXUIElementCreateApplication(pid)
        applyCuaDriverAXMessagingTimeout(root)

        // Cue Chromium/Electron apps to turn on their web accessibility tree.
        // Native AppKit apps can stall on the AXManualAccessibility writes, so
        // keep this Chromium-only instead of probing every app optimistically.
        try await activateAccessibilityIfNeeded(
            pid: pid,
            root: root,
            bundleId: running.bundleIdentifier
        )
`,
    "safe accessibility activation call",
  );

  patched = replaceRequired(
    patched,
    `    private func activateAccessibilityIfNeeded(
        pid: Int32,
        root: AXUIElement
    ) async throws {
        // Already did the one-shot pump + observer for this pid.
`,
    `    private func activateAccessibilityIfNeeded(
        pid: Int32,
        root: AXUIElement,
        bundleId: String?
    ) async throws {
        guard shouldAssertAccessibilityForAXClientSignals(
            pid: pid,
            bundleId: bundleId
        ) else { return }
        // Already did the one-shot pump + observer for this pid.
`,
    "Chromium-only accessibility activation signature",
  );

  patched = replaceRequired(
    patched,
    `        pumpRunLoopForActivation(duration: 0.5)
    }

    /// Pump the current thread's CFRunLoop for roughly \`duration\` seconds.
`,
    `        pumpRunLoopForActivation(duration: 0.5)
    }

    private nonisolated func shouldAssertAccessibilityForAXClientSignals(
        pid: Int32,
        bundleId: String?
    ) -> Bool {
        if ElectronJS.isElectron(pid: pid) { return true }
        switch bundleId {
        case "com.google.Chrome",
             "com.google.Chrome.canary",
             "com.microsoft.edgemac",
             "com.microsoft.edgemac.Canary",
             "com.brave.Browser",
             "com.operasoftware.Opera",
             "com.vivaldi.Vivaldi":
            return true
        default:
            return false
        }
    }

    /// Pump the current thread's CFRunLoop for roughly \`duration\` seconds.
`,
    "Chromium app detection helper",
  );

  patched = replaceRequired(
    patched,
    `    ) {
        guard depth <= AppStateEngine.maxDepth else { return }

        let role = attributeString(element, "AXRole") ?? "?"
`,
    `    ) {
        guard depth <= AppStateEngine.maxDepth else { return }
        guard nextIndex < AppStateEngine.maxActionableElements else { return }
        applyCuaDriverAXMessagingTimeout(element)

        let role = attributeString(element, "AXRole") ?? "?"
`,
    "renderTree guard",
  );

  patched = replaceRequired(
    patched,
    `        let enabled = attributeBool(element, "AXEnabled")
        let actions = actionNames(of: element)

        let indent = String(repeating: "  ", count: depth)
`,
    `        let enabled = attributeBool(element, "AXEnabled")
        let actions = actionNames(of: element)
        let descendantSummary =
            (role == "AXRow" && (title?.isEmpty ?? true) && (value?.isEmpty ?? true) && (description?.isEmpty ?? true))
            ? descendantLabelSummary(of: element)
            : nil

        let indent = String(repeating: "  ", count: depth)
`,
    "AXRow descendant summary",
  );

  patched = replaceRequired(
    patched,
    `        line += role
        if let t = title, !t.isEmpty { line += " \\"\\(t)\\"" }
        if let v = value, !v.isEmpty, v.count < 120 { line += " = \\"\\(v)\\"" }
`,
    `        line += role
        if let t = title, !t.isEmpty { line += " \\"\\(t)\\"" }
        else if let s = descendantSummary, !s.isEmpty { line += " \\"\\(s)\\"" }
        if let v = value, !v.isEmpty, v.count < 120 { line += " = \\"\\(v)\\"" }
`,
    "AXRow label rendering",
  );

  for (const [needle, replacement, label] of [
    [
      `    private func windows(of appRoot: AXUIElement) -> [AXUIElement] {
        var value: CFTypeRef?
`,
      `    private func windows(of appRoot: AXUIElement) -> [AXUIElement] {
        applyCuaDriverAXMessagingTimeout(appRoot)
        var value: CFTypeRef?
`,
      "AX timeout for windows",
    ],
    [
      `    private func isMenuOpen(_ menu: AXUIElement) -> Bool {
        var value: CFTypeRef?
`,
      `    private func isMenuOpen(_ menu: AXUIElement) -> Bool {
        applyCuaDriverAXMessagingTimeout(menu)
        var value: CFTypeRef?
`,
      "AX timeout for menu visibility",
    ],
    [
      `    private func attributeString(_ element: AXUIElement, _ attribute: String) -> String? {
        var value: CFTypeRef?
`,
      `    private func attributeString(_ element: AXUIElement, _ attribute: String) -> String? {
        applyCuaDriverAXMessagingTimeout(element)
        var value: CFTypeRef?
`,
      "AX timeout for string attributes",
    ],
    [
      `    private func attributeBool(_ element: AXUIElement, _ attribute: String) -> Bool? {
        var value: CFTypeRef?
`,
      `    private func attributeBool(_ element: AXUIElement, _ attribute: String) -> Bool? {
        applyCuaDriverAXMessagingTimeout(element)
        var value: CFTypeRef?
`,
      "AX timeout for boolean attributes",
    ],
    [
      `    private func actionNames(of element: AXUIElement) -> [String] {
        var names: CFArray?
`,
      `    private func actionNames(of element: AXUIElement) -> [String] {
        applyCuaDriverAXMessagingTimeout(element)
        var names: CFArray?
`,
      "AX timeout for action names",
    ],
    [
      `    private func children(of element: AXUIElement) -> [AXUIElement] {
        var value: CFTypeRef?
`,
      `    private func children(of element: AXUIElement) -> [AXUIElement] {
        applyCuaDriverAXMessagingTimeout(element)
        var value: CFTypeRef?
`,
      "AX timeout for children",
    ],
  ]) {
    patched = replaceRequired(patched, needle, replacement, label);
  }

  patched = replaceRequired(
    patched,
    `    /// Standard AX actions come through as simple strings like \`AXPress\`.
`,
    `    private func descendantLabelSummary(of element: AXUIElement) -> String? {
        var labels: [String] = []
        collectDescendantLabels(element, depth: 0, maxDepth: 2, labels: &labels)
        let joined = labels
            .map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }
            .filter { !$0.isEmpty }
            .prefix(4)
            .joined(separator: " ")
        return joined.isEmpty ? nil : joined
    }

    private func collectDescendantLabels(
        _ element: AXUIElement,
        depth: Int,
        maxDepth: Int,
        labels: inout [String]
    ) {
        if labels.count >= 4 || depth > maxDepth { return }
        let role = attributeString(element, "AXRole") ?? ""
        if role == "AXStaticText" || role == "AXButton" || role == "AXImage" {
            for attribute in ["AXTitle", "AXValue", "AXDescription"] {
                if let text = attributeString(element, attribute), !text.isEmpty {
                    labels.append(text)
                    if labels.count >= 4 { return }
                    break
                }
            }
        }
        if depth == maxDepth { return }
        for child in children(of: element) {
            collectDescendantLabels(child, depth: depth + 1, maxDepth: maxDepth, labels: &labels)
            if labels.count >= 4 { return }
        }
    }

    /// Standard AX actions come through as simple strings like \`AXPress\`.
`,
    "AXRow descendant label helpers",
  );

  return patched;
}

export function patchCuaDriverClickToolSource(source) {
  if (source.includes(CUA_CLICK_PATCH_SENTINEL)) return source;
  let patched = source;

  patched = replaceRequired(
    patched,
    `"enum": ["press", "show_menu", "pick", "confirm", "cancel", "open"],`,
    `"enum": ["press", "show_menu", "show_default_ui", "pick", "confirm", "cancel", "open"],`,
    "ClickTool action schema",
  );

  patched = replaceRequired(
    patched,
    `"show_menu": "AXShowMenu",
        "pick": "AXPick",`,
    `"show_menu": "AXShowMenu",
        "show_default_ui": "AXShowDefaultUI",
        "pick": "AXPick",`,
    "ClickTool AXShowDefaultUI action mapping",
  );

  patched = replaceRequired(
    patched,
    "Other values:\n                  `show_menu` (right-click equivalent), `pick` (open a",
    "Other values:\n                  `show_menu` (right-click equivalent), `show_default_ui`\n                  (select a row/list item via AXShowDefaultUI), `pick` (open a",
    "ClickTool action description",
  );

  return patched;
}

// macOS 14 SDK 的 SwiftUI / os.Logger 缺少较新 SDK 的并发标注。
// 补充实际 UI 隔离并兼容旧 Logger 声明，保留 Swift 6 的语言模式和运行逻辑。
export function patchCuaDriverSDKCompatibilitySource(source, fileName) {
  if (fileName === "AgentCursorView.swift" && !source.includes("@MainActor\npublic struct AgentCursorView")) {
    return replaceRequired(source, "public struct AgentCursorView: View {", "@MainActor\npublic struct AgentCursorView: View {", "macOS 14 cursor actor isolation");
  }
  if (fileName === "PermissionsGate.swift" && !source.includes("@MainActor\nprivate struct PermissionsRootView")) {
    return replaceRequired(source, "private struct PermissionsRootView: View {", "@MainActor\nprivate struct PermissionsRootView: View {", "macOS 14 permissions actor isolation");
  }
  if (["ClickMarkerRenderer.swift", "RecordingRenderer.swift", "VideoRecorder.swift"].includes(fileName) && !source.includes("@preconcurrency import os")) {
    return replaceRequired(source, "\nimport os\n", "\n@preconcurrency import os\n", "macOS 14 Logger Sendable compatibility");
  }
  if (fileName === "SystemFocusStealPreventer.swift") {
    let patched = source;
    if (!patched.includes("let restorePID = restoreTo.processIdentifier")) {
      patched = replaceRequired(patched, "let delayNs = suppressionDelayNs\n        let task = Task.detached {", "let delayNs = suppressionDelayNs\n        let restorePID = restoreTo.processIdentifier\n        let task = Task.detached {", "sendable restoration process identity");
      patched = replaceRequired(patched, "_ = restoreTo.activate(options: [])", "_ = NSRunningApplication(processIdentifier: restorePID)?.activate(options: [])", "main actor application lookup");
    }
    if (!patched.includes("restorePID: pid_t")) {
      const anchor = "    @discardableResult\n    public func beginSuppression(";
      patched = replaceRequired(patched, anchor, `    // 跨 actor 只传递 PID，避免旧 SDK 的 NSRunningApplication 非 Sendable 声明。
    public func beginSuppression(targetPid: pid_t, restorePID: pid_t) async -> SuppressionHandle {
        let handle = SuppressionHandle()
        if let restoreTo = NSRunningApplication(processIdentifier: restorePID) {
            dispatcher.add(handle: handle, targetPid: targetPid, restoreTo: restoreTo)
        }
        return handle
    }

${anchor}`, "pid-scoped suppression overload");
    }
    return patched;
  }
  if (fileName === "LaunchAppTool.swift" && !source.includes("let priorFrontmostPID =")) {
    let patched = replaceRequired(source, "let priorFrontmost = await MainActor.run {\n                    NSWorkspace.shared.frontmostApplication\n                }", "let priorFrontmostPID = await MainActor.run {\n                    NSWorkspace.shared.frontmostApplication?.processIdentifier\n                }", "sendable frontmost process identity");
    for (const [needle, replacement] of [
      ["if let priorFrontmost {", "if let priorFrontmostPID {"],
      ["priorFrontmost != nil", "priorFrontmostPID != nil"],
      ["priorFrontmost?.processIdentifier != info.pid", "priorFrontmostPID != info.pid"],
      ["if shouldSuppress, let handle, let priorFrontmost {", "if shouldSuppress, let handle, let priorFrontmostPID {"],
      ["_ = priorFrontmost.activate(options: [])", "_ = NSRunningApplication(processIdentifier: priorFrontmostPID)?.activate(options: [])"],
    ]) patched = replaceRequired(patched, needle, replacement, "launch suppression process identity");
    patched = patched.replaceAll("restoreTo: priorFrontmost", "restorePID: priorFrontmostPID");
    patched = patched.replaceAll("NSWorkspace.shared.frontmostApplication\n                    }", "NSWorkspace.shared.frontmostApplication?.processIdentifier\n                    }");
    patched = patched.replaceAll("frontmostNow?.processIdentifier", "frontmostNow").replaceAll("frontmostAfter?.processIdentifier", "frontmostAfter");
    return patched;
  }
  return source;
}

// 私有类存在不代表其工厂方法可用。macOS 14.1 缺少该 selector，直接
// objc_msgSend 会抛出无法由 Swift catch 捕获的异常，导致整个 daemon 退出。
// 方法不可用时让上游返回 false，沿用 KeyboardInput 的公开按键投递回退。
export function patchCuaDriverEventCompatibilitySource(source) {
  const sentinel = "class_getClassMethod(messageClass, factorySelector)";
  if (source.includes(sentinel)) return source;
  let patched = replaceRequired(source, "        return Resolved(\n            postToPid: postToPid,", `        // 中文兼容补丁：先验证类方法，避免旧 macOS 的 unrecognized selector 崩溃。
        let factorySelector = NSSelectorFromString("messageWithEventRecord:pid:version:")
        guard class_getClassMethod(messageClass, factorySelector) != nil else { return nil }

        return Resolved(
            postToPid: postToPid,`, "SkyLight authentication method availability");
  return replaceRequired(patched, `            factorySelector: NSSelectorFromString(
                "messageWithEventRecord:pid:version:")`, "            factorySelector: factorySelector", "checked SkyLight factory selector");
}

// 某些自绘控件会对不支持的 AXSelectedText 写入返回 success，却没有输入文字。
// 先检查可写性，明确拒绝时抛回上游的 AXInputError，启用既有键盘输入回退。
export function patchCuaDriverTextInputSource(source) {
  if (source.includes("AXUIElementIsAttributeSettable(element, attribute as CFString")) return source;
  return replaceRequired(source,
    "        let result = AXUIElementSetAttributeValue(element, attribute as CFString, value)",
    `        if attribute == "AXSelectedText" {
            var settable = DarwinBoolean(false)
            let availability = AXUIElementIsAttributeSettable(element, attribute as CFString, &settable)
            guard availability == .success, settable.boolValue else {
                let code = availability == .success ? AXError.attributeUnsupported.rawValue : availability.rawValue
                throw AXInputError.setAttributeFailed(attribute: attribute, code: code)
            }
        }
        let result = AXUIElementSetAttributeValue(element, attribute as CFString, value)`,
    "AX selected text write capability");
}

// 文本输入控件通常不支持 AXPress；点击输入框时使用明确的 AXFocused 属性，
// 避免调用 AXConfirm 意外提交表单，也不退化成前台坐标点击。
export function patchCuaDriverTextFocusSource(source) {
  if (source.includes('"focus": "AXFocused"')) return source;
  let patched = replaceRequired(source, '"enum": ["press", "show_menu",', '"enum": ["press", "focus", "show_menu",', "text input focus action schema");
  patched = replaceRequired(patched, '"press": "AXPress",', '"press": "AXPress",\n        "focus": "AXFocused",', "text input focus action mapping");
  patched = replaceRequired(patched, "                try AXInput.performAction(axAction, on: element)", `                if axAction == "AXFocused" {
                    try AXInput.setAttribute("AXFocused", on: element, value: kCFBooleanTrue as CFTypeRef)
                    guard AXInput.boolAttribute("AXFocused", of: element) == true else {
                        throw AXInputError.setAttributeFailed(attribute: "AXFocused", code: AXError.cannotComplete.rawValue)
                    }
                } else {
                    try AXInput.performAction(axAction, on: element)
                }`, "semantic input focus without AXPress");
  return replaceRequired(patched, "if !advertisedActions.contains(axAction) {", 'if axAction != "AXFocused" && !advertisedActions.contains(axAction) {', "focus property is not an advertised action");
}

export function applyCuaDriverSourcePatches({ scratchPath, sdkVersion } = {}) {
  const appStatePath = path.join(scratchPath, CUA_APP_STATE_RELATIVE_PATH);
  if (!fs.existsSync(appStatePath)) {
    throw new Error(`[computer-use-helper] Cua AppState.swift not found at ${appStatePath}`);
  }
  const clickToolPath = path.join(scratchPath, CUA_CLICK_TOOL_RELATIVE_PATH);
  if (!fs.existsSync(clickToolPath)) {
    throw new Error(`[computer-use-helper] Cua ClickTool.swift not found at ${clickToolPath}`);
  }

  let patchedAny = false;
  const appStateSource = fs.readFileSync(appStatePath, "utf8");
  const patchedAppState = patchCuaDriverAppStateSource(appStateSource);
  if (patchedAppState !== appStateSource) {
    fs.chmodSync(appStatePath, 0o644);
    fs.writeFileSync(appStatePath, patchedAppState);
    console.log("[computer-use-helper] patched Cua AX tree walk for bounded background snapshots");
    patchedAny = true;
  }

  const clickToolSource = fs.readFileSync(clickToolPath, "utf8");
  const patchedClickTool = patchCuaDriverTextFocusSource(patchCuaDriverClickToolSource(clickToolSource));
  if (patchedClickTool !== clickToolSource) {
    fs.chmodSync(clickToolPath, 0o644);
    fs.writeFileSync(clickToolPath, patchedClickTool);
    console.log("[computer-use-helper] patched Cua ClickTool for AXShowDefaultUI row actions");
    patchedAny = true;
  }

  // 这是运行时系统兼容问题，与编译时 SDK 版本无关，所有构建均需应用。
  const eventPostPath = path.join(scratchPath, CUA_EVENT_POST_RELATIVE_PATH);
  const eventPostSource = fs.readFileSync(eventPostPath, "utf8");
  const patchedEventPost = patchCuaDriverEventCompatibilitySource(eventPostSource);
  if (patchedEventPost !== eventPostSource) {
    fs.chmodSync(eventPostPath, 0o644);
    fs.writeFileSync(eventPostPath, patchedEventPost);
    console.log("[computer-use-helper] patched SkyLight runtime method availability");
    patchedAny = true;
  }
  const inputPath = path.join(path.dirname(eventPostPath), "AXInput.swift");
  const inputSource = fs.readFileSync(inputPath, "utf8");
  const patchedInput = patchCuaDriverTextInputSource(inputSource);
  if (patchedInput !== inputSource) {
    fs.chmodSync(inputPath, 0o644);
    fs.writeFileSync(inputPath, patchedInput);
    console.log("[computer-use-helper] patched AX text input capability check");
    patchedAny = true;
  }

  if (Number(sdkVersion?.split(".")[0]) < 15) {
    for (const relative of ["Cursor/AgentCursorView.swift", "Permissions/PermissionsGate.swift", "Recording/ClickMarkerRenderer.swift", "Recording/Render/RecordingRenderer.swift", "Recording/VideoRecorder.swift", "Focus/SystemFocusStealPreventer.swift"]) {
      const file = path.join(scratchPath, "checkouts", "cua", "libs", "cua-driver", "Sources", "CuaDriverCore", relative);
      const source = fs.readFileSync(file, "utf8");
      const patched = patchCuaDriverSDKCompatibilitySource(source, path.basename(file));
      if (patched !== source) {
        fs.chmodSync(file, 0o644);
        fs.writeFileSync(file, patched);
        console.log(`[computer-use-helper] patched macOS ${sdkVersion} SDK compatibility: ${relative}`);
        patchedAny = true;
      }
    }
    const launchFile = path.join(scratchPath, "checkouts", "cua", "libs", "cua-driver", "Sources", "CuaDriverServer", "Tools", "LaunchAppTool.swift");
    const launchSource = fs.readFileSync(launchFile, "utf8");
    const patchedLaunch = patchCuaDriverSDKCompatibilitySource(launchSource, "LaunchAppTool.swift");
    if (patchedLaunch !== launchSource) {
      fs.chmodSync(launchFile, 0o644);
      fs.writeFileSync(launchFile, patchedLaunch);
      patchedAny = true;
      console.log("[computer-use-helper] patched macOS 14 SDK launch focus restoration");
    }
  }

  return patchedAny;
}

function run(command, args, options = {}) {
  execFileSync(command, args, { stdio: "inherit", ...options });
}

function read(command, args, options = {}) {
  return execFileSync(command, args, { encoding: "utf8", ...options }).trim();
}

// Package.swift 本身声明 6.0，但锁定的 swift-log 1.12.0 和
// swift-collections 1.4.1 都要求 Swift tools 6.2；必须检查传递依赖的实际下限。
export function validateSwiftVersion(versionOutput) {
  const match = versionOutput.match(/Swift version (\d+)\.(\d+)/);
  const major = Number(match?.[1] || 0);
  const minor = Number(match?.[2] || 0);
  if (major < 6 || (major === 6 && minor < 2)) {
    throw new Error(`当前锁定的原生依赖需要 Swift 6.2+，当前为：${versionOutput.split("\n")[0]}。请配置兼容工具链或已有 helper 的路径。`);
  }
  return { major, minor };
}

export function buildComputerUseHelper({
  rootDir = path.resolve(__dirname, ".."),
  platform = process.platform,
  env = process.env,
  arch = env.HANA_COMPUTER_USE_HELPER_ARCH || process.arch,
} = {}) {
  if (!shouldBuildComputerUseHelper({ platform })) {
    console.log(`[computer-use-helper] skipped on ${platform}`);
    return { skipped: true };
  }

  const packageDir = path.join(rootDir, "desktop", "native", "HanaComputerUseHelper");
  const swiftArch = swiftArchForNodeArch(arch);
  // 先检查本地编译器，避免下载依赖后才发现 Swift 版本不兼容。
  const swift = env.HANA_COMPUTER_USE_SWIFT_PATH || "swift";
  const swiftVersion = read(swift, ["--version"], { cwd: rootDir, env });
  validateSwiftVersion(swiftVersion);
  const scratchPath = swiftBuildScratchPath({ rootDir, arch });
  const baseArgs = [
    "--package-path",
    packageDir,
    "--scratch-path",
    scratchPath,
    "--force-resolved-versions",
    "-c",
    "release",
    "--arch",
    swiftArch,
    "--product",
    "hana-computer-use-helper",
  ];

  console.log(`[computer-use-helper] building for ${swiftArch}`);
  run(swift, ["package", "resolve", "--force-resolved-versions", "--package-path", packageDir, "--scratch-path", scratchPath], { cwd: rootDir, env });
  const sdkVersion = read("xcrun", ["--sdk", "macosx", "--show-sdk-version"], { cwd: rootDir, env });
  applyCuaDriverSourcePatches({ scratchPath, sdkVersion });
  run(swift, ["build", ...baseArgs], { cwd: rootDir, env });

  const binPath = read(swift, ["build", "--show-bin-path", ...baseArgs], { cwd: rootDir, env });
  const source = path.join(binPath, "hana-computer-use-helper");
  if (!fs.existsSync(source)) {
    throw new Error(`[computer-use-helper] build did not produce ${source}`);
  }

  const outDir = computerUseHelperOutputDir({ rootDir, osName: "mac", arch });
  fs.rmSync(outDir, { recursive: true, force: true });
  fs.mkdirSync(outDir, { recursive: true });
  const target = path.join(outDir, "hana-computer-use-helper");
  fs.copyFileSync(source, target);
  fs.chmodSync(target, 0o755);
  console.log(`[computer-use-helper] copied ${target}`);
  return { skipped: false, target };
}

if (process.argv[1] && pathToFileURL(process.argv[1]).href === import.meta.url) {
  try {
    buildComputerUseHelper({ arch: resolveComputerUseHelperBuildArch() });
  } catch (err) {
    console.error(err?.stack || err?.message || String(err));
    process.exitCode = 1;
  }
}
