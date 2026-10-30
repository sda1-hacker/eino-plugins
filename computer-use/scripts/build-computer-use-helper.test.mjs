import assert from "node:assert/strict";
import test from "node:test";
import { validateSwiftVersion, swiftArchForNodeArch, patchCuaDriverSDKCompatibilitySource } from "./build-computer-use-helper.mjs";

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
