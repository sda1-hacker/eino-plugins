// 独立的 AppKit 测试应用：只操作临时窗口，避免验证时修改用户正在编辑的文档。
import AppKit

final class FixtureDelegate: NSObject, NSApplicationDelegate {
    var window: NSWindow!
    var input: NSTextField!
    var button: NSButton!
    var clicks = 0

    func applicationDidFinishLaunching(_ notification: Notification) {
        window = NSWindow(contentRect: NSRect(x: 120, y: 180, width: 640, height: 280),
                          styleMask: [.titled, .closable], backing: .buffered, defer: false)
        window.title = "Eino Computer Use 临时验证窗口"
        window.isReleasedWhenClosed = false

        let label = NSTextField(labelWithString: "此窗口仅用于自动化验证，关闭后不保存任何内容。")
        label.frame = NSRect(x: 24, y: 210, width: 590, height: 30)
        window.contentView!.addSubview(label)

        input = NSTextField(frame: NSRect(x: 24, y: 140, width: 590, height: 36))
        input.placeholderString = "等待 Computer Use 输入"
        input.setAccessibilityLabel("Eino 验证输入框")
        input.setAccessibilityIdentifier("eino-validation-input")
        window.contentView!.addSubview(input)

        button = NSButton(title: "Eino 验证按钮", target: self, action: #selector(clicked))
        button.frame = NSRect(x: 24, y: 60, width: 360, height: 44)
        button.bezelStyle = .rounded
        button.setAccessibilityIdentifier("eino-validation-button")
        window.contentView!.addSubview(button)

        window.makeKeyAndOrderFront(nil)
        window.makeFirstResponder(input)
        NSApplication.shared.activate(ignoringOtherApps: true)
    }

    @objc func clicked() {
        clicks += 1
        button.title = "Eino 验证按钮，已点击 \(clicks) 次"
    }
}

let app = NSApplication.shared
let delegate = FixtureDelegate()
app.delegate = delegate
app.setActivationPolicy(.regular)
app.run()
