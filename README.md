# Eino Plugins

面向 [CloudWeGo Eino](https://github.com/cloudwego/eino) 的 Go 组件集合。每个组件都是独立 Go module，各自维护依赖、测试和版本；使用者只需安装需要的组件。

## 组件

| 组件 | 模块路径 | 说明 | Go 版本 |
| --- | --- | --- | --- |
| [computer-use](computer-use/) | `github.com/sda1-hacker/eino-plugins/computer-use` | 桌面应用观察、截图与语义操作，支持 Eino 多模态工具接口 | 1.23+ |
| [sandboxfs](sandboxfs/) | `github.com/sda1-hacker/eino-plugins/sandboxfs` | 工作区文件操作与沙箱命令执行，接入 Eino filesystem | 1.27.1+ |

组件发布后，在自己的 Go 项目中安装：

```sh
go get github.com/sda1-hacker/eino-plugins/computer-use
go get github.com/sda1-hacker/eino-plugins/sandboxfs
```

也可以使用 `@v0.1.0` 等版本选择器。版本按组件分别发布，以上命令需要对应代码已经推送到公开仓库；本地整理不会自动发布。

导入示例：

```go
import (
    computeruse "github.com/sda1-hacker/eino-plugins/computer-use"
    "github.com/sda1-hacker/eino-plugins/sandboxfs"
)
```

`computer-use` 是模块目录名，Go 包名为 `computeruse`。

## 仓库结构

```text
eino-plugins/
├── .git/                 # 整个仓库共用一份 Git 元数据
├── README.md
├── LICENSE
├── docs/
│   ├── releasing.md      # 组件独立发版与 pkg.go.dev 收录流程
│   └── repository-migration.md
├── computer-use/
│   ├── go.mod            # module .../eino-plugins/computer-use
│   ├── go.sum
│   ├── doc.go
│   ├── computeruse.go    # 公开包直接位于模块根目录
│   ├── assets/
│   ├── desktop/
│   ├── examples/
│   ├── scripts/
│   ├── docs/
│   ├── LICENSE
│   └── NOTICE
└── sandboxfs/
    ├── go.mod            # module .../eino-plugins/sandboxfs
    ├── go.sum
    ├── backend.go
    ├── sandbox_*.go
    ├── example/
    └── LICENSE
```

根目录只负责组织仓库；每个组件目录中的 `go.mod` 定义可独立下载的模块。新增组件时，在其目录创建自己的 `go.mod`、`README.md`、`LICENSE` 和根包源码，模块路径使用 `github.com/sda1-hacker/eino-plugins/<组件目录>`。

## 本地开发

进入对应组件执行 Go 命令：

```sh
cd computer-use
GOWORK=off go test -race ./...
GOWORK=off go vet ./...
go run ./examples/eino
```

测试 `sandboxfs` 时先进入 `sandboxfs`。真实桌面和真实沙箱的验证方法见各组件 README，普通单元测试无需调用模型服务。

需要同时开发多个组件时，可在仓库根目录生成本地工作区：

```sh
go work init ./computer-use ./sandboxfs
```

`go.work` 与 `go.work.sum` 已被忽略。它们只服务本地联调，不参与组件分发；发版前使用 `GOWORK=off` 单独验证组件，避免本地工作区掩盖依赖问题。

## 独立发版

| 组件版本 | 仓库 Git 标签 | 使用者安装命令 |
| --- | --- | --- |
| computer-use `v0.1.0` | `computer-use/v0.1.0` | `go get github.com/sda1-hacker/eino-plugins/computer-use@v0.1.0` |
| sandboxfs `v0.1.0` | `sandboxfs/v0.1.0` | `go get github.com/sda1-hacker/eino-plugins/sandboxfs@v0.1.0` |

子目录 module 的标签需要带目录前缀，规则见 [Go Modules Reference](https://go.dev/ref/mod#vcs-version)。完整命令与 pkg.go.dev 收录方法见 [发布说明](docs/releasing.md)。

## 许可证

采用 [Apache License 2.0](LICENSE)。各组件保留独立的许可证文件；上游移植来源与修改说明见组件内的 NOTICE。
