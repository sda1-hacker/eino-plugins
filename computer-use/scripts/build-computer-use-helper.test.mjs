import assert from "node:assert/strict";
import test from "node:test";
import { validateSwiftVersion, swiftArchForNodeArch, patchCuaDriverSDKCompatibilitySource, patchCuaDriverEventCompatibilitySource, patchCuaDriverTextInputSource, patchCuaDriverTextFocusSource } from "./build-computer-use-helper.mjs";

test("根据锁定的传递依赖拒绝旧 Swift 编译器", () => {
  for (const version of ["Apple Swift version 5.9.2", "Swift version 6.0.3", "Swift version 6.1.3", "unrecognised compiler"]) {
    assert.throws(() => validateSwiftVersion(version), /Swift 6.2\+/);
  }
  assert.deepEqual(validateSwiftVersion("Apple Swift version 6.2.4 (swift-6.2.4-RELEASE)"), { major: 6, minor: 2 });
  assert.deepEqual(validateSwiftVersion("Swift version 6.3.1"), { major: 6, minor: 3 });
  assert.deepEqual(validateSwiftVersion("Swift version 7.0.0"), { major: 7, minor: 0 });
});

test("架构只能映射到受支持的输出目录", () => {
  assert.equal(swiftArchForNodeArch("x64"), "x86_64");
  assert.equal(swiftArchForNodeArch("arm64"), "arm64");
  assert.throws(() => swiftArchForNodeArch("../../other-directory"));
});

test("旧 SDK 的并发标注补丁可重复应用", () => {
  for (const [file, input, expected] of [
    ["AgentCursorView.swift", "public struct AgentCursorView: View {", "@MainActor\npublic struct"],
    ["PermissionsGate.swift", "private struct PermissionsRootView: View {", "@MainActor\nprivate struct"],
    ["ClickMarkerRenderer.swift", "import AppKit\nimport os\n", "@preconcurrency import os"],
    ["RecordingRenderer.swift", "import Foundation\nimport os\n", "@preconcurrency import os"],
    ["VideoRecorder.swift", "import Foundation\nimport os\n", "@preconcurrency import os"],
  ]) {
    const patched = patchCuaDriverSDKCompatibilitySource(input, file);
    assert.ok(patched.includes(expected));
    assert.equal(patchCuaDriverSDKCompatibilitySource(patched, file), patched);
  }
});

test("延迟恢复焦点只捕获进程 ID，在主 actor 中查找应用", () => {
  const source = "    @discardableResult\n    public func beginSuppression(\nlet delayNs = suppressionDelayNs\n        let task = Task.detached {\n_ = restoreTo.activate(options: [])\n}";
  const patched = patchCuaDriverSDKCompatibilitySource(source, "SystemFocusStealPreventer.swift");
  assert.ok(patched.includes("let restorePID = restoreTo.processIdentifier"));
  assert.ok(patched.includes("NSRunningApplication(processIdentifier: restorePID)?.activate"));
  assert.ok(patched.includes("restorePID: pid_t"));
  assert.equal(patchCuaDriverSDKCompatibilitySource(patched, "SystemFocusStealPreventer.swift"), patched);
});

test("SkyLight 在构造私有消息前验证类方法，重复构建不会叠加补丁", () => {
  const source = `        return Resolved(
            postToPid: postToPid,
            factorySelector: NSSelectorFromString(
                "messageWithEventRecord:pid:version:")
        )`;
  const patched = patchCuaDriverEventCompatibilitySource(source);
  assert.ok(patched.indexOf("class_getClassMethod(messageClass, factorySelector)") < patched.indexOf("return Resolved("));
  assert.ok(patched.includes("factorySelector: factorySelector"));
  assert.equal(patchCuaDriverEventCompatibilitySource(patched), patched);
  // 上游源码形状发生变化时必须明确失败，避免构建成功却漏掉崩溃修复。
  assert.throws(() => patchCuaDriverEventCompatibilitySource("unknown upstream source"), /patch anchor not found/);
});

test("AX 文本写入先检查可写性，不支持时触发既有的键盘回退", () => {
  const source = "        let result = AXUIElementSetAttributeValue(element, attribute as CFString, value)";
  const patched = patchCuaDriverTextInputSource(source);
  assert.ok(patched.includes('if attribute == "AXSelectedText"'));
  assert.ok(patched.indexOf("AXUIElementIsAttributeSettable") < patched.indexOf("AXUIElementSetAttributeValue"));
  assert.ok(patched.includes("throw AXInputError.setAttributeFailed"));
  assert.equal(patchCuaDriverTextInputSource(patched), patched);
  assert.throws(() => patchCuaDriverTextInputSource("unknown upstream source"), /patch anchor not found/);
});

test("输入框的语义点击只聚焦，不触发确认或坐标点击", () => {
  const source = `"enum": ["press", "show_menu", "pick"],
"press": "AXPress",
                try AXInput.performAction(axAction, on: element)
if !advertisedActions.contains(axAction) {`;
  const patched = patchCuaDriverTextFocusSource(source);
  assert.ok(patched.includes('"focus": "AXFocused"'));
  assert.ok(patched.includes('AXInput.boolAttribute("AXFocused", of: element) == true'));
  assert.equal(patchCuaDriverTextFocusSource(patched), patched);
  assert.throws(() => patchCuaDriverTextFocusSource("unknown upstream source"), /patch anchor not found/);
});
