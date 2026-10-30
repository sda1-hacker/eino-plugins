# 独立组件发布

本仓库采用多个独立 Go module。每个组件都有自己的 `go.mod`、依赖与版本，根目录不创建 `go.mod`。组件可以使用不同的 Eino 版本和最低 Go 版本；使用者的项目同时依赖多个组件时，仍按 Go 的模块版本选择规则解析共同依赖。

## 发布 computer-use

以下以首次发布 `v0.1.0` 为例。确认代码已审核并提交后再执行推送与打标签；这些命令是发布操作，本次目录迁移没有执行它们。

从仓库根目录进入模块并检查：

```sh
cd computer-use
GOWORK=off go mod tidy
GOWORK=off CGO_ENABLED=0 go test ./...
GOWORK=off go test -race ./...
GOWORK=off go vet ./...
GOWORK=off go list .
```

最后一条命令应输出：

```text
github.com/sda1-hacker/eino-plugins/computer-use
```

确认 `go mod tidy` 的改动已提交，返回仓库根目录，推送提交并创建组件版本标签：

```sh
cd ..
git push -u origin main
git tag -a computer-use/v0.1.0 -m "computer-use v0.1.0"
git push origin computer-use/v0.1.0
```

`computer-use/v0.1.0` 是仓库标签；使用者填写的 Go 版本仍为 `v0.1.0`。这是 [Go 子目录模块的版本映射规则](https://go.dev/ref/mod#vcs-version)。发布 `sandboxfs` 时使用 `sandboxfs/v0.1.0`，检查命令在 `sandboxfs` 目录执行。

模块达到 v2 或更高主版本时，`module` 与导入路径需要增加 `/v2` 等主版本后缀。已经发布的版本保持不变，修复通过新版本发布，详见 [Go 发布指南](https://go.dev/doc/modules/publishing)。

## 验证远程下载与 pkg.go.dev 收录

代码和标签在公开的 `github.com/sda1-hacker/eino-plugins` 仓库可访问后，在仓库外新建临时目录，验证公开版本：

```sh
mkdir /tmp/eino-plugins-consumer
cd /tmp/eino-plugins-consumer
go mod init example.com/eino-plugins-consumer
GOWORK=off GOPROXY=https://proxy.golang.org go get github.com/sda1-hacker/eino-plugins/computer-use@v0.1.0
GOWORK=off go list github.com/sda1-hacker/eino-plugins/computer-use
```

从 Go 模块代理下载会触发索引流程，也可以在 [computer-use 的 pkg.go.dev 页面](https://pkg.go.dev/github.com/sda1-hacker/eino-plugins/computer-use)点击 Request。收录方式见 [pkg.go.dev 官方说明](https://pkg.go.dev/about#adding-a-package)。这不是上传压缩包到 pkg.go.dev；分发来源是公开仓库与 Go 模块代理。

本地 `replace` 只能验证导入和构建，无法证明公开版本已可下载。发布验证时确保临时消费者没有指向本地源码的 `replace`。

## 模块中应包含的文件

每个组件保留 README、`go.mod`、`go.sum` 和编译所需资源，并保留适用的许可证与来源声明。Go 包说明可直接放在入口文件的 package 注释中，例如 computer-use 的 `computeruse.go`，不必单独建立 `doc.go`。

computer-use 的 macOS 默认后端随 Go 程序编译，通过 purego 调用系统框架，支持 `CGO_ENABLED=0`，不需要 Swift / Node.js / 外部 helper。组件不提供外部 macOS 驱动兼容入口。Windows helper 仍使用 `go:embed` 携带 PowerShell 脚本。`go get` 不会安装系统权限或外部运行环境。

构建缓存、IDE 配置、原生二进制和本地 `go.work` 均不提交。新增模块时不要在组件目录再次初始化 `.git`。
