// 在独立临时 AppKit 窗口中运行 Go + Eino + Cua 的真实验证；finally 只清理自建进程。
import fs from "node:fs";
import path from "node:path";
import { execFileSync, spawn, spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
const positional = process.argv.slice(2).find(argument => !argument.startsWith("--"));
const helper = path.resolve(positional || path.join(root, "dist-computer-use", `mac-${process.arch}`, "hana-computer-use-helper"));
if (process.platform !== "darwin") throw new Error("真实原生验证需要 macOS");
if (!fs.existsSync(helper)) throw new Error(`没有找到 helper：${helper}，请先构建`);
const validation = path.join(root, ".cache", "validation");
const app = path.join(validation, "EinoComputerUseFixture.app");
const contents = path.join(app, "Contents");
const executable = path.join(contents, "MacOS", "EinoComputerUseFixture");
fs.mkdirSync(path.dirname(executable), { recursive: true });
fs.writeFileSync(path.join(contents, "Info.plist"), `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleIdentifier</key><string>local.eino.computeruse.validation</string>
<key>CFBundleName</key><string>EinoComputerUseFixture</string>
<key>CFBundleExecutable</key><string>EinoComputerUseFixture</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>NSHighResolutionCapable</key><true/>
</dict></plist>`);

const compiler = process.env.HANA_COMPUTER_USE_SWIFTC_PATH || (process.env.HANA_COMPUTER_USE_SWIFT_PATH
  ? path.join(path.dirname(process.env.HANA_COMPUTER_USE_SWIFT_PATH), "swiftc") : "swiftc");
execFileSync(compiler, ["-module-cache-path", path.join(validation, "fixture-module-cache"), path.join(root, "testdata", "macos-fixture", "main.swift"), "-o", executable], { cwd: root, stdio: "inherit" });
const fixture = spawn(executable, [], { cwd: root, stdio: "ignore" });
let exitCode = 1;
try {
  await new Promise((resolve, reject) => {
    const timer = setTimeout(resolve, 1800);
    fixture.once("error", error => { clearTimeout(timer); reject(error); });
    fixture.once("exit", code => { clearTimeout(timer); reject(new Error(`测试窗口启动失败：${code}`)); });
  });
  const args = ["run", "./examples/verify", "-helper", helper, "-report", path.join(validation, "native-verification.json")];
  if (process.argv.includes("--request-permissions")) args.push("-request-permissions");
  const result = spawnSync("go", args, { cwd: root, stdio: "inherit", env: process.env });
  if (result.error) throw result.error;
  exitCode = result.status ?? 1;
} finally {
  fixture.kill("SIGTERM");
}
process.exitCode = exitCode;
