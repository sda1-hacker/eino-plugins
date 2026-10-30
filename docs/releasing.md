# 独立组件发布

本仓库采用多个独立 Go module。每个组件都有自己的 `go.mod`、依赖与版本，根目录不创建 `go.mod`。组件可以使用不同的 Eino 版本和最低 Go 版本；使用者的项目同时依赖多个组件时，仍按 Go 的模块版本选择规则解析共同依赖。

## 发布 computer-use

以下以首次发布 `v0.1.0` 为例。确认代码已审核并提交后再执行推送与打标签；这些命令是发布操作，本次目录迁移没有执行它们。

从仓库根目录进入模块并检查：

```sh
cd computer-use
GOWORK=off go mod tidy
GOWORK=off go test -race ./...
GOWORK=off go vet ./...
node --test scripts/build-computer-use-helper.test.mjs
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

`computer-use/v0.1.0` 是仓库标签；使用者填写的 Go 版本仍为 `v0.1.0`。这是 [Go 子目录模块的版本映射规则](https://go.dev/ref/mod#vcs-version)。发布 `sandboxfs` 时使用 `sandboxfs/v0.1.0`，检查命令在 `sandboxfs` 目录执行，无需运行 computer-use 的 Node.js 检查。

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

每个组件保留自己的 `LICENSE`、README、`go.mod`、`go.sum` 和编译所需资源；有上游移植代码时同时保留 NOTICE。Go 包说明由带中文注释的 `doc.go` 或包注释生成。

computer-use 的 Windows helper 使用 `go:embed`，随模块源码携带；macOS helper 的 Swift 源码和构建脚本保留在该模块中，原生可执行文件需要由宿主构建或显式配置。`go get` 不会自动编译 Swift、安装 Node.js 或设置系统权限。

构建缓存、IDE 配置、原生二进制和本地 `go.work` 均不提交。新增模块时不要在组件目录再次初始化 `.git`。
