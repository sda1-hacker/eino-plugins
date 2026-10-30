// 默认验证 CGO_ENABLED=0 的 Go + Eino 后端；Swift 仅用来创建独立的测试窗口。
// finally 只清理本脚本创建的测试进程。
import fs from "node:fs";
import path from "node:path";
import { execFileSync, spawn, spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";
import { randomInt } from "node:crypto";

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "..");
for (const argument of process.argv.slice(2)) {
  if (!["--model", "--request-permissions"].includes(argument)) throw new Error(`未知参数：${argument}`);
}
const modelMode = process.argv.includes("--model");
if (modelMode) {
  for (const name of ["OPENAI_API_KEY", "OPENAI_MODEL_NAME", "OPENAI_BASE_URL"]) {
    if (!process.env[name]) throw new Error(`环境变量 ${name} 未配置`);
  }
}
// 验证码只传给测试进程和独立断言器，不放进模型提示词或工具文本。
const environment = { ...process.env, CGO_ENABLED: "0", EINO_VALIDATION_VISUAL_CODE: String(randomInt(100000, 1000000)) };
if (process.platform !== "darwin") throw new Error("真实原生验证需要 macOS");
const validation = path.join(root, ".cache", "validation");
const reportPath = path.join(validation, modelMode ? "model-verification.json" : "native-verification.json");
fs.mkdirSync(validation, { recursive: true });
// 构建或调用失败时不能把上一次 passed=true 的报告当成本次结果。
fs.rmSync(reportPath, { force: true });
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

const compiler = process.env.EINO_COMPUTER_USE_SWIFTC || "swiftc";
execFileSync(compiler, ["-module-cache-path", path.join(validation, "fixture-module-cache"), path.join(root, "testdata", "macos-fixture", "main.swift"), "-o", executable], { cwd: root, stdio: "inherit" });
const fixture = spawn(executable, [], { cwd: root, stdio: "ignore", env: environment });
let exitCode = 1;
try {
  await new Promise((resolve, reject) => {
    const timer = setTimeout(resolve, 1800);
    fixture.once("error", error => { clearTimeout(timer); reject(error); });
    fixture.once("exit", code => { clearTimeout(timer); reject(new Error(`测试窗口启动失败：${code}`)); });
  });
  let workdir = root;
  let args = ["run", "./testdata/verification", "-report", reportPath];
  if (modelMode) {
    // 模型厂商适配器放在临时独立模块，组件本身不增加模型服务依赖。
    workdir = path.join(validation, "model-runner");
    fs.mkdirSync(workdir, { recursive: true });
    fs.copyFileSync(path.join(root, "testdata", "model-runner", "main.go"), path.join(workdir, "main.go"));
    fs.copyFileSync(path.join(root, "testdata", "model-runner", "main_test.go"), path.join(workdir, "main_test.go"));
    fs.writeFileSync(path.join(workdir, "go.mod"), `module example.com/eino-computer-use-model-verification

go 1.25.0

require (
    github.com/sda1-hacker/eino-plugins/computer-use v0.0.0
    github.com/cloudwego/eino-ext/components/model/openai v0.1.13
)

replace github.com/sda1-hacker/eino-plugins/computer-use => ${JSON.stringify(root)}
`);
    const selfCheck = spawnSync("go", ["test", "-mod=mod", "."], { cwd: workdir, stdio: "inherit", env: environment });
    if (selfCheck.error || selfCheck.status !== 0) throw new Error("模型验收器自检失败");
    args = ["run", "-mod=mod", ".", "-report", reportPath];
  }
  if (process.argv.includes("--request-permissions")) args.push("-request-permissions");
  const result = spawnSync("go", args, { cwd: workdir, stdio: "inherit", env: environment });
  if (result.error) throw result.error;
  exitCode = result.status ?? 1;
} finally {
  if (fixture.exitCode === null && fixture.signalCode === null) {
    await new Promise(resolve => {
      const timer = setTimeout(() => fixture.kill("SIGKILL"), 2000);
      fixture.once("exit", () => { clearTimeout(timer); resolve(); });
      fixture.kill("SIGTERM");
    });
  }
  const cleanup = { fixtureExited: fixture.exitCode !== null || fixture.signalCode !== null, externalHelperUsed: false };
  if (fs.existsSync(reportPath)) {
    const report = JSON.parse(fs.readFileSync(reportPath, "utf8"));
    report.backend = "macos:ax";
    report.cgoEnabled = false;
    report.cleanup = cleanup;
    report.passed = Boolean(report.passed && cleanup.fixtureExited);
    fs.writeFileSync(reportPath, JSON.stringify(report, null, 2) + "\n", { mode: 0o600 });
    if (!report.passed) exitCode = 1;
  } else {
    fs.writeFileSync(reportPath, JSON.stringify({ passed: false, reason: "验证程序未生成本轮报告", cleanup }, null, 2) + "\n", { mode: 0o600 });
    exitCode = 1;
  }
  console.log("验证资源清理：" + JSON.stringify(cleanup));
}
process.exitCode = exitCode;
