// 独立的 AppKit 测试应用：只操作临时窗口，避免验证时修改用户正在编辑的文档。
import AppKit

// 不提供 AXSelectedText 的自绘输入控件，用于真实验证 CGEvent 中文输入回退。
final class KeyboardOnlyInput: NSView {
    var text = ""
    var commits = 0
    var onChange: (() -> Void)?
    override var acceptsFirstResponder: Bool { true }
    override func isAccessibilityElement() -> Bool { true }
    override func accessibilityRole() -> NSAccessibility.Role? { .textField }
    override func accessibilityLabel() -> String? { "Eino 键盘回退输入框" }
    override func accessibilityValue() -> Any? { text }
    override func isAccessibilitySelectorAllowed(_ selector: Selector) -> Bool {
        // 自绘控件只接受真实键盘事件，不提供 AX 的文本选择 / 插入接口。
        if ["setAccessibilitySelectedText:", "accessibilitySelectedText"].contains(NSStringFromSelector(selector)) { return false }
        return super.isAccessibilitySelectorAllowed(selector)
    }
    override func isAccessibilityFocused() -> Bool { window?.firstResponder === self }
    override func setAccessibilityFocused(_ focused: Bool) {
        if focused { window?.makeFirstResponder(self) }
    }
    override func accessibilityPerformPress() -> Bool { window?.makeFirstResponder(self) ?? false }
    override func keyDown(with event: NSEvent) {
        switch event.keyCode {
        case 36: commits += 1
        case 51: if !text.isEmpty { text.removeLast() }
        default: text += event.characters ?? ""
        }
        needsDisplay = true
        onChange?()
        NSAccessibility.post(element: self, notification: .valueChanged)
    }
    override func draw(_ dirtyRect: NSRect) {
        NSColor.textBackgroundColor.setFill()
        bounds.fill()
        (text.isEmpty ? "等待键盘回退输入" : text as NSString).draw(
            at: NSPoint(x: 6, y: 8),
            withAttributes: [.font: NSFont.systemFont(ofSize: 17), .foregroundColor: NSColor.textColor])
    }
}

final class SecondaryButton: NSButton {
    var showMenu: (() -> Void)?
    override func accessibilityPerformShowMenu() -> Bool {
        showMenu?()
        return true
    }
}

// 验证模型确实读取图片：随机视觉码只绘制在画布上，不进入 AX 树。
final class VisualChallenge: NSView {
    override func isAccessibilityElement() -> Bool { false }
    override func draw(_ dirtyRect: NSRect) {
        NSColor(calibratedRed: 1, green: 0.93, blue: 0.65, alpha: 1).setFill()
        bounds.fill()
        let code = ProcessInfo.processInfo.environment["EINO_VALIDATION_VISUAL_CODE"] ?? "123456"
        ("视觉验证码：" + code as NSString).draw(at: NSPoint(x: 10, y: 12),
            withAttributes: [.font: NSFont.monospacedSystemFont(ofSize: 21, weight: .bold), .foregroundColor: NSColor.black])
    }
}

final class FixtureDelegate: NSObject, NSApplicationDelegate, NSMenuDelegate {
    var window: NSWindow!
    var input: NSTextField!
    var button: NSButton!
    var clicks = 0
    var keyCommits = 0
    var secondaryActions = 0
    let keyStatus = NSTextField(labelWithString: "Eino 按键确认：0 次")
    let fallbackStatus = NSTextField(labelWithString: "Eino 回退按键确认：0 次")
    let secondaryStatus = NSTextField(labelWithString: "Eino 次级动作：0 次，菜单已关闭")
    let scrollStatus = NSTextField(labelWithString: "Eino 滚动偏移：0")
    var scrollView: NSScrollView!
    var contextMenu: NSMenu!

    func applicationDidFinishLaunching(_ notification: Notification) {
        window = NSWindow(contentRect: NSRect(x: 120, y: 100, width: 740, height: 650),
                          styleMask: [.titled, .closable], backing: .buffered, defer: false)
        window.title = "Eino Computer Use 临时验证窗口"
        window.isReleasedWhenClosed = false

        let label = NSTextField(labelWithString: "此窗口仅用于自动化验证，关闭后不保存任何内容。")
        label.frame = NSRect(x: 24, y: 600, width: 690, height: 30)
        window.contentView!.addSubview(label)
        let visual = VisualChallenge(frame: NSRect(x: 380, y: 540, width: 330, height: 48))
        window.contentView!.addSubview(visual)

        input = NSTextField(frame: NSRect(x: 24, y: 490, width: 690, height: 36))
        input.placeholderString = "等待 Computer Use 输入"
        input.setAccessibilityLabel("Eino 验证输入框")
        input.setAccessibilityIdentifier("eino-validation-input")
        input.target = self
        input.action = #selector(committed)
        window.contentView!.addSubview(input)
        keyStatus.frame = NSRect(x: 24, y: 458, width: 690, height: 24)
        window.contentView!.addSubview(keyStatus)

        let fallback = KeyboardOnlyInput(frame: NSRect(x: 24, y: 410, width: 690, height: 36))
        fallback.onChange = { [weak self, weak fallback] in
            self?.fallbackStatus.stringValue = "Eino 回退按键确认：\(fallback?.commits ?? 0) 次"
        }
        window.contentView!.addSubview(fallback)
        fallbackStatus.frame = NSRect(x: 24, y: 378, width: 690, height: 24)
        window.contentView!.addSubview(fallbackStatus)

        button = NSButton(title: "Eino 验证按钮", target: self, action: #selector(clicked))
        button.frame = NSRect(x: 24, y: 325, width: 330, height: 44)
        button.bezelStyle = .rounded
        button.setAccessibilityIdentifier("eino-validation-button")
        window.contentView!.addSubview(button)

        let secondary = SecondaryButton(title: "Eino 次级动作按钮", target: nil, action: nil)
        secondary.frame = NSRect(x: 380, y: 325, width: 330, height: 44)
        secondary.bezelStyle = .rounded
        contextMenu = NSMenu(title: "Eino 验证菜单")
        contextMenu.delegate = self
        contextMenu.addItem(NSMenuItem(title: "Eino 菜单验证项", action: nil, keyEquivalent: ""))
        secondary.showMenu = { [weak self, weak secondary] in
            guard let self, let secondary else { return }
            self.secondaryActions += 1
            // AX 调用先返回，再在主循环显示真实菜单，避免同步菜单跟踪阻塞调用。
            DispatchQueue.main.async {
                self.contextMenu.popUp(positioning: nil, at: NSPoint(x: 0, y: secondary.bounds.height), in: secondary)
            }
        }
        window.contentView!.addSubview(secondary)
        secondaryStatus.frame = NSRect(x: 24, y: 288, width: 690, height: 25)
        window.contentView!.addSubview(secondaryStatus)

        scrollView = NSScrollView(frame: NSRect(x: 24, y: 65, width: 690, height: 210))
        scrollView.hasVerticalScroller = true
        scrollView.borderType = .bezelBorder
        let document = NSTextView(frame: NSRect(x: 0, y: 0, width: 670, height: 2000))
        document.isRichText = false
        document.font = NSFont.systemFont(ofSize: 16)
        document.string = (1...100).map { "第 \($0) 行：Eino 原生滚动验证内容" }.joined(separator: "\n")
        document.setAccessibilityLabel("Eino 验证滚动内容")
        document.setSelectedRange(NSRange(location: 0, length: 0))
        scrollView.documentView = document
        scrollView.contentView.postsBoundsChangedNotifications = true
        NotificationCenter.default.addObserver(self, selector: #selector(scrolled), name: NSView.boundsDidChangeNotification, object: scrollView.contentView)
        window.contentView!.addSubview(scrollView)
        scrollStatus.frame = NSRect(x: 24, y: 26, width: 690, height: 25)
        window.contentView!.addSubview(scrollStatus)

        window.makeKeyAndOrderFront(nil)
        window.makeFirstResponder(input)
        NSApplication.shared.activate(ignoringOtherApps: true)
    }

    @objc func clicked() {
        clicks += 1
        button.title = "Eino 验证按钮，已点击 \(clicks) 次"
    }
    @objc func committed() {
        keyCommits += 1
        keyStatus.stringValue = "Eino 按键确认：\(keyCommits) 次"
    }
    @objc func scrolled() {
        scrollStatus.stringValue = "Eino 滚动偏移：\(Int(scrollView.contentView.bounds.origin.y))"
    }
    func menuWillOpen(_ menu: NSMenu) {
        secondaryStatus.stringValue = "Eino 次级动作：\(secondaryActions) 次，菜单已打开"
    }
    func menuDidClose(_ menu: NSMenu) {
        secondaryStatus.stringValue = "Eino 次级动作：\(secondaryActions) 次，菜单已关闭"
    }
}

let app = NSApplication.shared
let delegate = FixtureDelegate()
app.delegate = delegate
app.setActivationPolicy(.regular)
app.run()
