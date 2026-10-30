// Package computeruse 将 OpenHanako 的桌面控制能力封装为 Eino 工具。
//
// 工具支持应用枚举、控制租约、截图与无障碍元素观察，以及基于元素的语义操作。
// NewComputerUseTool 返回实现 Eino EnhancedInvokableTool 的多模态工具；
// 如需兼容字符串工具接口，可使用 Tool.AsTextTool。
//
// 模块导入路径为 github.com/sda1-hacker/eino-plugins/computer-use，
// Go 包名为 computeruse。macOS 需要另行构建或配置原生 helper，
// Windows UI Automation 脚本随 Go 包内嵌；NewMockProvider 可用于无需桌面的验证。
package computeruse
