# 仓库迁移记录

迁移日期：2026-10-30。

新仓库根目录为 `/Users/sda1_hacker/Desktop/humbert/eino-plugins`，配置的 origin 为 `https://github.com/sda1-hacker/eino-plugins.git`。迁移仅初始化本地 `main` 分支，未创建提交、标签或执行推送。

## computer-use

保留模块路径 `github.com/sda1-hacker/eino-plugins/computer-use`。将原来模块内多余的 `computer-use/` 源码层级移到模块根目录，包括单元测试和 `assets/`；更新示例与 README 的导入路径，公开包名保持 `computeruse`。

原先的真实桌面验证报告保留原始时间与旧路径，作为历史证据，没有修改成迁移后的路径。

## sandboxfs 历史备份

在修改 sandboxfs 文件和移出其 `.git` 前，已备份到仓库外：

```text
/Users/sda1_hacker/Desktop/humbert/eino-plugins-backups/sandboxfs-20261030-115421/
├── snapshot/                   # 迁移前完整工作树及 .git
├── original.git/               # 从原目录移出的原始 Git 元数据
├── sandboxfs-history.bundle    # 原分支与标签的可迁移历史
├── status.txt                  # 原暂存区及未提交状态
├── staged.patch                # 原暂存区改动
└── unstaged.patch              # 原未暂存改动
```

已逐文件校验 `.git` 副本的 SHA-256，并通过 `git fsck --full` 和 `git bundle verify` 检查。副本保留了原来的暂存区、未提交改动、引用和配置；bundle 额外保留可移植的已提交历史。原远程为 `https://github.com/sda1-hacker/eino-sandboxfs.git`，原 main 提交为 `e8613ea`，包含原 `v0.1.0` 标签。

新仓库的 Git 日志从未来的首次提交开始；旧日志完整保留在备份中。要查看旧工作树状态，可对备份的 `snapshot` 运行 `git status`。要单独恢复已提交历史，可将 bundle 克隆到新目录，例如：

```sh
git clone /Users/sda1_hacker/Desktop/humbert/eino-plugins-backups/sandboxfs-20261030-115421/sandboxfs-history.bundle /tmp/sandboxfs-history
```

原工作区自定义示例和未提交业务改动仍保留在新目录；本次只调整模块引用、文档中的命令路径和模块许可证。新的 `sandboxfs/v0.1.0` 标签可独立于旧仓库的 `v0.1.0` 发布。

## 迁移后的验证

两个模块均设置 `GOWORK=off` 分别验证：

| 检查 | computer-use | sandboxfs |
| --- | --- | --- |
| `go mod tidy -diff` | 无差异 | 无差异 |
| `go test -race -count=1 ./...` | 50 个测试 / 子测试通过 | 33 个测试 / 子测试通过，11 个跳过 |
| `go vet ./...` | 通过 | 通过 |
| Windows / Linux amd64 交叉编译 | 通过 | 通过 |
| 模块压缩包资源与许可证 | 通过 | 通过 |
| 仓库外消费者 `go get`、导入和运行 | 通过 | 通过 |

computer-use 的 4 个 Node.js 构建预检测试、直接调用示例和 Eino ToolsNode 示例也已通过。sandboxfs 跳过项包含其他平台与需显式开启的真实沙箱测试；本次没有重跑真实桌面或真实沙箱操作。

分发验证使用 `golang.org/x/mod/zip` 创建并检查模块压缩包，排除 Git 元数据、IDE 文件、缓存与原生二进制。临时本地代理提供 `v0.0.0-reorg.1`，两份仓库外消费者分别执行 `go get`，从独立临时模块缓存加载源码，没有本地 `replace`。computer-use 验证了真实 Eino 图片 part，sandboxfs 验证了临时目录中的中文文件读写。

机器报告保存在 `/private/tmp/eino-plugins-distribution-xihxyup2/report.json`。上述版本仅用于本地验证，未在根仓库创建标签或发布到远程；公开下载和 pkg.go.dev 收录仍需执行发布流程。
