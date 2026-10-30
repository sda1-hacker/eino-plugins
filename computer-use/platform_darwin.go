//go:build darwin

package computeruse

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"structs"
	"sync"
	"syscall"
	"time"
	"unicode/utf16"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/cstrings"
	"github.com/ebitengine/purego/objc"
)

// 所有框架均由 macOS 自带；没有 import C、外部命令或额外动态库。
type macPoint struct {
	_    structs.HostLayout
	X, Y float64
}
type macSize struct {
	_    structs.HostLayout
	W, H float64
}
type macRect struct {
	_      structs.HostLayout
	Origin macPoint
	Size   macSize
}

type macSystemAPI struct {
	cfRelease        func(uintptr)
	cfRetain         func(uintptr) uintptr
	cfType           func(uintptr) uintptr
	cfStringType     func() uintptr
	cfNumberType     func() uintptr
	cfBoolType       func() uintptr
	cfStringCreate   func(uintptr, string, uint32) uintptr
	cfStringLength   func(uintptr) int64
	cfStringCopy     func(uintptr, *byte, int64, uint32) bool
	cfArrayCount     func(uintptr) int64
	cfArrayValue     func(uintptr, int64) uintptr
	cfDictValue      func(uintptr, uintptr) uintptr
	cfNumberValue    func(uintptr, int32, unsafe.Pointer) bool
	cfBoolValue      func(uintptr) bool
	cfDataCreate     func(uintptr, int64) uintptr
	cfDataLength     func(uintptr) int64
	cfDataBytes      func(uintptr) *byte
	axTrusted        func() bool
	axRequest        func(uintptr) bool
	axApplication    func(int32) uintptr
	axCopy           func(uintptr, uintptr, *uintptr) int32
	axActions        func(uintptr, *uintptr) int32
	axPerform        func(uintptr, uintptr) int32
	axSet            func(uintptr, uintptr, uintptr) int32
	axSettable       func(uintptr, uintptr, *bool) int32
	axTimeout        func(uintptr, float32) int32
	axValue          func(uintptr, uint32, unsafe.Pointer) bool
	axPID            func(uintptr, *int32) int32
	windowInfo       func(uint32, uint32) uintptr
	capture          func(macRect, uint32, uint32, uint32) uintptr
	screenAccess     func() bool
	screenRequest    func() bool
	imageDestination func(uintptr, uintptr, uintptr, uintptr) uintptr
	imageAdd         func(uintptr, uintptr, uintptr)
	imageFinalize    func(uintptr) bool
	keyEvent         func(uintptr, uint16, bool) uintptr
	keyUnicode       func(uintptr, uintptr, *uint16)
	keyFlags         func(uintptr, uint64)
	keyPost          func(int32, uintptr)
}

var macAPIOnce sync.Once
var sharedMacAPI *macSystemAPI
var macAPIError error

func loadMacSystemAPI() (*macSystemAPI, error) {
	macAPIOnce.Do(func() {
		a := &macSystemAPI{}
		libraries := []struct {
			name      string
			functions map[string]any
		}{
			{"CoreFoundation", map[string]any{
				"CFRelease": &a.cfRelease, "CFRetain": &a.cfRetain, "CFGetTypeID": &a.cfType,
				"CFStringGetTypeID": &a.cfStringType, "CFNumberGetTypeID": &a.cfNumberType, "CFBooleanGetTypeID": &a.cfBoolType,
				"CFStringCreateWithCString": &a.cfStringCreate, "CFStringGetLength": &a.cfStringLength, "CFStringGetCString": &a.cfStringCopy,
				"CFArrayGetCount": &a.cfArrayCount, "CFArrayGetValueAtIndex": &a.cfArrayValue, "CFDictionaryGetValue": &a.cfDictValue,
				"CFNumberGetValue": &a.cfNumberValue, "CFBooleanGetValue": &a.cfBoolValue,
				"CFDataCreateMutable": &a.cfDataCreate, "CFDataGetLength": &a.cfDataLength, "CFDataGetBytePtr": &a.cfDataBytes,
			}},
			{"ApplicationServices", map[string]any{
				"AXIsProcessTrusted": &a.axTrusted, "AXIsProcessTrustedWithOptions": &a.axRequest,
				"AXUIElementCreateApplication": &a.axApplication, "AXUIElementCopyAttributeValue": &a.axCopy,
				"AXUIElementCopyActionNames": &a.axActions, "AXUIElementPerformAction": &a.axPerform,
				"AXUIElementSetAttributeValue": &a.axSet, "AXUIElementIsAttributeSettable": &a.axSettable,
				"AXUIElementSetMessagingTimeout": &a.axTimeout, "AXValueGetValue": &a.axValue, "AXUIElementGetPid": &a.axPID,
			}},
			{"CoreGraphics", map[string]any{
				"CGWindowListCopyWindowInfo": &a.windowInfo, "CGWindowListCreateImage": &a.capture,
				"CGPreflightScreenCaptureAccess": &a.screenAccess, "CGRequestScreenCaptureAccess": &a.screenRequest,
				"CGEventCreateKeyboardEvent": &a.keyEvent, "CGEventKeyboardSetUnicodeString": &a.keyUnicode,
				"CGEventSetFlags": &a.keyFlags, "CGEventPostToPid": &a.keyPost,
			}},
			{"ImageIO", map[string]any{
				"CGImageDestinationCreateWithData": &a.imageDestination, "CGImageDestinationAddImage": &a.imageAdd, "CGImageDestinationFinalize": &a.imageFinalize,
			}},
			{"AppKit", nil},
		}
		for _, library := range libraries {
			handle, err := purego.Dlopen("/System/Library/Frameworks/"+library.name+".framework/"+library.name, purego.RTLD_NOW|purego.RTLD_GLOBAL)
			if err != nil {
				macAPIError = err
				return
			}
			for name, function := range library.functions {
				symbol, err := purego.Dlsym(handle, name)
				if err != nil {
					macAPIError = fmt.Errorf("系统 API %s 不可用：%w", name, err)
					return
				}
				purego.RegisterFunc(function, symbol)
			}
		}
		sharedMacAPI = a
	})
	if macAPIError != nil {
		return nil, &ComputerUseError{Code: ErrProviderUnavailable, Message: "无法加载 macOS 自带框架", Cause: macAPIError}
	}
	return sharedMacAPI, nil
}

type macAXDriver struct {
	api        *macSystemAPI
	keys       map[string]uintptr
	elements   map[string]uintptr
	pid        int32
	window     uint32
	generation uint64
}

func newNativeMacOSDriver() (nativeMacOSDriver, error) {
	api, err := loadMacSystemAPI()
	if err != nil {
		return nil, err
	}
	return &macAXDriver{api: api, keys: map[string]uintptr{}, elements: map[string]uintptr{}}, nil
}

// Cocoa 自动释放池与创建它的系统线程绑定，操作期间禁止 Go 调度迁移线程。
func macThread() func() {
	runtime.LockOSThread()
	pool := objc.ID(objc.GetClass("NSAutoreleasePool")).Send(objc.RegisterName("new"))
	return func() { pool.Send(objc.RegisterName("drain")); runtime.UnlockOSThread() }
}
func nsString(text string) objc.ID {
	return objc.ID(objc.GetClass("NSString")).Send(objc.RegisterName("stringWithUTF8String:"), text)
}
func workspace() objc.ID {
	return objc.ID(objc.GetClass("NSWorkspace")).Send(objc.RegisterName("sharedWorkspace"))
}
func (d *macAXDriver) key(name string) uintptr {
	if ref := d.keys[name]; ref != 0 {
		return ref
	}
	ref := d.api.cfStringCreate(0, name, 0x08000100)
	d.keys[name] = ref
	return ref
}

func (d *macAXDriver) text(ref uintptr) string {
	if ref == 0 || d.api.cfType(ref) != d.api.cfStringType() {
		return ""
	}
	length := d.api.cfStringLength(ref)
	if length < 0 || length > 1<<20 {
		return ""
	}
	buffer := make([]byte, length*4+1)
	if !d.api.cfStringCopy(ref, &buffer[0], int64(len(buffer)), 0x08000100) {
		return ""
	}
	end := bytes.IndexByte(buffer, 0)
	if end < 0 {
		end = len(buffer)
	}
	text := string(buffer[:end])
	if len(text) > 16384 {
		text = string([]rune(text)[:min(len([]rune(text)), 4096)]) + "…"
	}
	return text
}
func (d *macAXDriver) scalar(ref uintptr) any {
	if ref == 0 {
		return nil
	}
	switch d.api.cfType(ref) {
	case d.api.cfStringType():
		return d.text(ref)
	case d.api.cfBoolType():
		return d.api.cfBoolValue(ref)
	case d.api.cfNumberType():
		var n float64
		if d.api.cfNumberValue(ref, 6, unsafe.Pointer(&n)) {
			return n
		}
	}
	return nil
}
func (d *macAXDriver) dict(ref uintptr, key string) uintptr {
	return d.api.cfDictValue(ref, d.key(key))
}
func (d *macAXDriver) number(ref uintptr) float64 { n, _ := d.scalar(ref).(float64); return n }

// attribute 返回 Copy 语义的 CF 对象，调用者必须释放；数组 / 字典中的借用值不释放。
func (d *macAXDriver) attribute(element uintptr, name string) uintptr {
	var value uintptr
	if d.api.axCopy(element, d.key(name), &value) != 0 {
		return 0
	}
	return value
}
func (d *macAXDriver) attrText(element uintptr, name string) string {
	value := d.attribute(element, name)
	if value == 0 {
		return ""
	}
	defer d.api.cfRelease(value)
	return d.text(value)
}
func (d *macAXDriver) attrBool(element uintptr, name string, fallback bool) bool {
	value := d.attribute(element, name)
	if value == 0 {
		return fallback
	}
	defer d.api.cfRelease(value)
	if d.api.cfType(value) != d.api.cfBoolType() {
		return fallback
	}
	return d.api.cfBoolValue(value)
}
func (d *macAXDriver) bounds(element uintptr) *Bounds {
	p, s := d.attribute(element, "AXPosition"), d.attribute(element, "AXSize")
	if p == 0 || s == 0 {
		if p != 0 {
			d.api.cfRelease(p)
		}
		if s != 0 {
			d.api.cfRelease(s)
		}
		return nil
	}
	defer d.api.cfRelease(p)
	defer d.api.cfRelease(s)
	var point macPoint
	var size macSize
	if !d.api.axValue(p, 1, unsafe.Pointer(&point)) || !d.api.axValue(s, 2, unsafe.Pointer(&size)) {
		return nil
	}
	return &Bounds{X: point.X, Y: point.Y, Width: size.W, Height: size.H}
}

func (d *macAXDriver) Status(ctx context.Context, prompt bool) (ProviderStatus, error) {
	if err := ctx.Err(); err != nil {
		return ProviderStatus{}, err
	}
	defer macThread()()
	if prompt {
		truth := objc.ID(objc.GetClass("NSNumber")).Send(objc.RegisterName("numberWithBool:"), true)
		options := objc.ID(objc.GetClass("NSDictionary")).Send(objc.RegisterName("dictionaryWithObject:forKey:"), truth, nsString("AXTrustedCheckOptionPrompt"))
		d.api.axRequest(uintptr(options))
		if !d.api.screenAccess() {
			d.api.screenRequest()
		}
	}
	return ProviderStatus{Available: true, Permissions: []Permission{{Name: "accessibility", Granted: d.api.axTrusted()}, {Name: "screen-recording", Granted: d.api.screenAccess()}}, Details: map[string]any{"backend": "purego", "externalHelper": false}}, nil
}
func (d *macAXDriver) requireAX() error {
	if !d.api.axTrusted() {
		return failure(ErrOSPermissionDenied, "需要为实际运行的 Go 程序或终端 / IDE 开启 macOS 辅助功能权限")
	}
	return nil
}

func (d *macAXDriver) windows() map[int][]Window {
	out := map[int][]Window{}
	list := d.api.windowInfo(16, 0) // 全部窗口，排除桌面背景。
	if list == 0 {
		return out
	}
	defer d.api.cfRelease(list)
	for i, n := int64(0), d.api.cfArrayCount(list); i < n; i++ {
		info := d.api.cfArrayValue(list, i)
		if d.number(d.dict(info, "kCGWindowLayer")) != 0 {
			continue
		}
		bounds := d.dict(info, "kCGWindowBounds")
		if bounds == 0 {
			continue
		}
		b := Bounds{X: d.number(d.dict(bounds, "X")), Y: d.number(d.dict(bounds, "Y")), Width: d.number(d.dict(bounds, "Width")), Height: d.number(d.dict(bounds, "Height"))}
		if b.Width < 80 || b.Height < 60 {
			continue
		}
		pid := int(d.number(d.dict(info, "kCGWindowOwnerPID")))
		on, _ := d.scalar(d.dict(info, "kCGWindowIsOnscreen")).(bool)
		out[pid] = append(out[pid], Window{WindowID: strconv.Itoa(int(d.number(d.dict(info, "kCGWindowNumber")))), Title: d.text(d.dict(info, "kCGWindowName")), Bounds: &b, IsOnScreen: &on})
	}
	return out
}
func (d *macAXDriver) runningApps() []App {
	list := workspace().Send(objc.RegisterName("runningApplications"))
	windows := d.windows()
	var apps []App
	for i, n := uint64(0), objc.Send[uint64](list, objc.RegisterName("count")); i < n; i++ {
		running := list.Send(objc.RegisterName("objectAtIndex:"), i)
		pid := int(objc.Send[int32](running, objc.RegisterName("processIdentifier")))
		if objc.Send[int64](running, objc.RegisterName("activationPolicy")) != 0 && len(windows[pid]) == 0 {
			continue
		}
		id := cstrings.NSStringToString(running.Send(objc.RegisterName("bundleIdentifier")))
		if id == "" {
			id = "pid:" + strconv.Itoa(pid)
		}
		apps = append(apps, App{AppID: id, Name: cstrings.NSStringToString(running.Send(objc.RegisterName("localizedName"))), PID: pid, Active: objc.Send[bool](running, objc.RegisterName("isActive")), Windows: windows[pid]})
	}
	return apps
}
func (d *macAXDriver) ListApps(ctx context.Context) ([]App, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	defer macThread()()
	apps := d.runningApps()
	seen := map[string]bool{}
	for _, app := range apps {
		seen[strings.ToLower(app.AppID)] = true
	}
	home, _ := os.UserHomeDir()
	for _, root := range []string{"/Applications", "/Applications/Utilities", "/System/Applications", "/System/Applications/Utilities", filepath.Join(home, "Applications")} {
		entries, _ := os.ReadDir(root)
		for _, entry := range entries {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if !strings.HasSuffix(entry.Name(), ".app") {
				continue
			}
			bundle := objc.ID(objc.GetClass("NSBundle")).Send(objc.RegisterName("bundleWithPath:"), nsString(filepath.Join(root, entry.Name())))
			id := cstrings.NSStringToString(bundle.Send(objc.RegisterName("bundleIdentifier")))
			if id == "" || seen[strings.ToLower(id)] {
				continue
			}
			seen[strings.ToLower(id)] = true
			apps = append(apps, App{AppID: id, Name: strings.TrimSuffix(entry.Name(), ".app")})
		}
	}
	sort.SliceStable(apps, func(i, j int) bool {
		if (apps[i].PID > 0) != (apps[j].PID > 0) {
			return apps[i].PID > 0
		}
		return apps[i].Name < apps[j].Name
	})
	return apps, nil
}
func (d *macAXDriver) CreateLease(ctx context.Context, target Target) (ProviderLease, error) {
	if err := ctx.Err(); err != nil {
		return ProviderLease{}, err
	}
	if err := d.requireAX(); err != nil {
		return ProviderLease{}, err
	}
	defer macThread()()
	if strings.HasPrefix(target.AppID, "pid:") {
		target.PID, _ = strconv.Atoi(strings.TrimPrefix(target.AppID, "pid:"))
	}
	lookup := func() (ProviderLease, error) {
		for _, app := range d.runningApps() {
			if (target.PID > 0 && target.PID == app.PID) || (target.PID == 0 && strings.EqualFold(app.AppID, target.AppID)) || (target.PID == 0 && target.AppID == "" && strings.EqualFold(app.Name, target.Name)) {
				return macLease(app, app.Windows, target)
			}
		}
		return ProviderLease{}, failure(ErrTargetNotFound, "目标应用尚未运行或没有可用窗口")
	}
	if lease, err := lookup(); err == nil {
		return lease, nil
	}
	if target.PID > 0 {
		return ProviderLease{}, failure(ErrTargetNotFound, "指定进程没有可用窗口")
	}
	url := workspace().Send(objc.RegisterName("URLForApplicationWithBundleIdentifier:"), nsString(target.AppID))
	if url == 0 {
		return ProviderLease{}, failure(ErrTargetNotFound, "未找到应用："+target.AppID)
	}
	var nsError objc.ID
	configuration := objc.ID(objc.GetClass("NSDictionary")).Send(objc.RegisterName("dictionary"))
	launched := workspace().Send(objc.RegisterName("launchApplicationAtURL:options:configuration:error:"), url, uint64(512), configuration, &nsError)
	if launched == 0 {
		return ProviderLease{}, failure(ErrTargetNotFound, "无法启动应用："+target.AppID)
	}
	for attempt := 0; attempt < 30; attempt++ {
		if lease, err := lookup(); err == nil {
			return lease, nil
		}
		select {
		case <-ctx.Done():
			return ProviderLease{}, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	return ProviderLease{}, failure(ErrTargetNotFound, "应用启动后没有创建可用窗口")
}

func (d *macAXDriver) clearElements() {
	for _, element := range d.elements {
		d.api.cfRelease(element)
	}
	d.elements = map[string]uintptr{}
	d.pid = 0
	d.window = 0
}
func (d *macAXDriver) Close() error {
	d.clearElements()
	for _, key := range d.keys {
		d.api.cfRelease(key)
	}
	d.keys = map[string]uintptr{}
	return nil
}

// 系统窗口编号来自 CGWindow，AXWindow 通过公开的位置、尺寸和标题匹配。
func (d *macAXDriver) windowElement(pid int32, bounds *Bounds, title string) (uintptr, error) {
	app := d.api.axApplication(pid)
	if app == 0 {
		return 0, failure(ErrTargetNotFound, "无法访问目标进程")
	}
	defer d.api.cfRelease(app)
	d.api.axTimeout(app, 0.35)
	windows := d.attribute(app, "AXWindows")
	if windows == 0 {
		return 0, failure(ErrTargetNotFound, "应用未暴露无障碍窗口")
	}
	defer d.api.cfRelease(windows)
	var candidates []Window
	var refs []uintptr
	for i, n := int64(0), d.api.cfArrayCount(windows); i < n; i++ {
		window := d.api.cfArrayValue(windows, i)
		d.api.axTimeout(window, 0.35)
		candidates = append(candidates, Window{Title: d.attrText(window, "AXTitle"), Bounds: d.bounds(window)})
		refs = append(refs, window)
	}
	if index := matchMacWindow(candidates, Window{Title: title, Bounds: bounds}); index >= 0 {
		return d.api.cfRetain(refs[index]), nil
	}
	return 0, failure(ErrTargetNotFound, "无法将指定窗口与无障碍窗口匹配："+title)
}

// 截取指定窗口，再用 Go 图片库缩放和编码；不会截取整个桌面发送给模型。
func (d *macAXDriver) screenshot(window uint32) (Image, Display, error) {
	if !d.api.screenAccess() {
		return Image{}, Display{}, failure(ErrOSPermissionDenied, "需要 macOS 屏幕录制权限")
	}
	ref := d.api.capture(macRect{}, 8, window, 1|8)
	if ref == 0 {
		return Image{}, Display{}, failure(ErrProviderCrashed, "窗口截图失败，窗口可能不可见或已关闭")
	}
	defer d.api.cfRelease(ref)
	data := d.api.cfDataCreate(0, 0)
	if data == 0 {
		return Image{}, Display{}, failure(ErrProviderCrashed, "无法创建截图缓冲")
	}
	defer d.api.cfRelease(data)
	destination := d.api.imageDestination(data, d.key("public.png"), 1, 0)
	if destination == 0 {
		return Image{}, Display{}, failure(ErrProviderCrashed, "无法创建图片编码器")
	}
	defer d.api.cfRelease(destination)
	d.api.imageAdd(destination, ref, 0)
	if !d.api.imageFinalize(destination) {
		return Image{}, Display{}, failure(ErrProviderCrashed, "截图编码失败")
	}
	size := d.api.cfDataLength(data)
	if size <= 0 || size > 64<<20 {
		return Image{}, Display{}, failure(ErrProviderCrashed, "截图大小无效")
	}
	raw := append([]byte(nil), unsafe.Slice(d.api.cfDataBytes(data), int(size))...)
	decoded, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return Image{}, Display{}, err
	}
	original := decoded.Bounds()
	w, h := original.Dx(), original.Dy()
	if w > 1280 {
		nw, nh := 1280, max(1, h*1280/w)
		resized := image.NewRGBA(image.Rect(0, 0, nw, nh))
		for y := 0; y < nh; y++ {
			for x := 0; x < nw; x++ {
				resized.Set(x, y, decoded.At(original.Min.X+x*w/nw, original.Min.Y+y*h/nh))
			}
		}
		decoded = resized
		w, h = nw, nh
	}
	var encoded bytes.Buffer
	if err = jpeg.Encode(&encoded, decoded, &jpeg.Options{Quality: 85}); err != nil {
		return Image{}, Display{}, err
	}
	return Image{Type: "image", MIMEType: "image/jpeg", Data: base64.StdEncoding.EncodeToString(encoded.Bytes())}, Display{Bounds: Bounds{Width: float64(w), Height: float64(h)}, OriginalWidth: float64(original.Dx()), OriginalHeight: float64(original.Dy()), ScaleFactor: float64(w) / float64(original.Dx())}, nil
}

// 每次观察重新构建元素缓存，编号包含递增版本；静态文本只保留在 AX 树中。
func (d *macAXDriver) Observe(ctx context.Context, lease Lease) (Snapshot, error) {
	if err := d.requireAX(); err != nil {
		return Snapshot{}, err
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	defer macThread()()
	pid := int32(integer(lease.State["pid"]))
	wid := uint32(integer(lease.State["windowId"]))
	if pid <= 0 || integer(lease.State["windowId"]) <= 0 {
		return Snapshot{}, failure(ErrTargetNotFound, "租约缺少有效 PID/windowId")
	}
	var target *Window
	for _, window := range d.windows()[int(pid)] {
		if window.WindowID == strconv.Itoa(int(wid)) {
			copy := window
			target = &copy
			break
		}
	}
	if target == nil {
		return Snapshot{}, failure(ErrTargetNotFound, "目标窗口已关闭")
	}
	window, err := d.windowElement(pid, target.Bounds, target.Title)
	if err != nil {
		return Snapshot{}, err
	}
	defer d.api.cfRelease(window)
	d.clearElements()
	d.pid = pid
	d.window = wid
	d.generation++
	snapshot := Snapshot{AppID: lease.AppID, WindowID: lease.WindowID, Mode: "vision-native"}
	snapshot.Screenshot, snapshot.Display, err = d.screenshot(wid)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot.Display.ScreenBounds = target.Bounds
	var tree strings.Builder
	visited := map[uintptr]bool{}
	var walk func(uintptr, int) error
	count := 0
	walk = func(element uintptr, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > 8 || count >= 350 || len(snapshot.Elements) >= 80 || visited[element] {
			return nil
		}
		visited[element] = true
		count++
		d.api.axTimeout(element, 0.35)
		role := d.attrText(element, "AXRole")
		label := d.attrText(element, "AXTitle")
		if label == "" {
			label = d.attrText(element, "AXDescription")
		}
		valueRef := d.attribute(element, "AXValue")
		value := d.scalar(valueRef)
		if valueRef != 0 {
			d.api.cfRelease(valueRef)
		}
		actionsRef := uintptr(0)
		var actions []string
		if d.api.axActions(element, &actionsRef) == 0 && actionsRef != 0 {
			for i, n := int64(0), d.api.cfArrayCount(actionsRef); i < n; i++ {
				actions = append(actions, d.text(d.api.cfArrayValue(actionsRef, i)))
			}
			d.api.cfRelease(actionsRef)
		}
		identifier := d.attrText(element, "AXIdentifier")
		enabled := d.attrBool(element, "AXEnabled", true)
		actionable := len(actions) > 0 || role == "AXButton" || role == "AXTextField" || role == "AXTextArea" || role == "AXCheckBox" || role == "AXComboBox" || role == "AXRow" || role == "AXPopUpButton"
		id := ""
		if actionable {
			id = fmt.Sprintf("ax:%d:%d", d.generation, len(snapshot.Elements))
			d.elements[id] = d.api.cfRetain(element)
			snapshot.Elements = append(snapshot.Elements, Element{ElementID: id, Role: role, Label: label, Value: value, Enabled: enabled, Actions: actions, AutomationID: identifier, Bounds: d.bounds(element)})
			if d.attrBool(element, "AXFocused", false) {
				snapshot.FocusedElementID = id
			}
		}
		fmt.Fprintf(&tree, "%s- ", strings.Repeat("  ", depth))
		if id != "" {
			fmt.Fprintf(&tree, "[%s] ", id)
		}
		fmt.Fprintf(&tree, "%s %q", role, label)
		if value != nil {
			fmt.Fprintf(&tree, " = %q", fmt.Sprint(value))
		}
		if !enabled {
			tree.WriteString(" DISABLED")
		}
		if len(actions) > 0 {
			fmt.Fprintf(&tree, " actions=%v", actions)
		}
		tree.WriteByte('\n')
		children := d.attribute(element, "AXChildren")
		if role == "AXMenu" {
			// 关闭的菜单也可能暴露完整子树，只给可见菜单项分配操作编号。
			if children != 0 {
				d.api.cfRelease(children)
			}
			children = d.attribute(element, "AXVisibleChildren")
		}
		if children != 0 {
			defer d.api.cfRelease(children)
			for i, n := int64(0), d.api.cfArrayCount(children); i < n; i++ {
				if err := walk(d.api.cfArrayValue(children, i), depth+1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err = walk(window, 0); err != nil {
		d.clearElements()
		return Snapshot{}, err
	}
	app := d.api.axApplication(pid)
	if app != 0 {
		defer d.api.cfRelease(app)
		d.api.axTimeout(app, 0.35)
		menu := d.attribute(app, "AXMenuBar")
		if menu != 0 {
			err = walk(menu, 0)
			d.api.cfRelease(menu)
			if err != nil {
				return Snapshot{}, err
			}
		}
	}
	snapshot.AccessibilityTree = tree.String()
	return snapshot, nil
}

func (d *macAXDriver) element(ctx context.Context, lease Lease, action ResolvedAction) (uintptr, func(), error) {
	if err := ctx.Err(); err != nil {
		return 0, nil, err
	}
	pid := int32(integer(lease.State["pid"]))
	if pid != d.pid || uint32(integer(lease.State["windowId"])) != d.window {
		return 0, nil, failure(ErrStaleSnapshot, "原生元素缓存已失效，请重新观察")
	}
	if action.ElementID != "" {
		element := d.elements[action.ElementID]
		if element == 0 {
			return 0, nil, failure(ErrStaleSnapshot, "当前快照没有该元素")
		}
		var owner int32
		if d.api.axPID(element, &owner) != 0 || owner != pid {
			return 0, nil, failure(ErrTargetNotFound, "目标元素不属于当前应用")
		}
		return element, func() {}, nil
	}
	app := d.api.axApplication(pid)
	defer d.api.cfRelease(app)
	element := d.attribute(app, "AXFocusedUIElement")
	if element == 0 {
		return 0, nil, failure(ErrTargetNotFound, "应用没有聚焦元素")
	}
	return element, func() { d.api.cfRelease(element) }, nil
}
func (d *macAXDriver) setFocus(element uintptr) error {
	truth := objc.ID(objc.GetClass("NSNumber")).Send(objc.RegisterName("numberWithBool:"), true)
	if code := d.api.axSet(element, d.key("AXFocused"), uintptr(truth)); code != 0 {
		return failure(ErrCapabilityUnsupported, fmt.Sprintf("无法聚焦元素（AX %d）", code))
	}
	if !d.attrBool(element, "AXFocused", false) {
		return failure(ErrCapabilityUnsupported, "目标元素没有接受焦点，请重新观察应用状态")
	}
	return nil
}
func (d *macAXDriver) perform(element uintptr, name string) error {
	if code := d.api.axPerform(element, d.key(name)); code != 0 {
		return failure(ErrCapabilityUnsupported, fmt.Sprintf("元素不支持 %s（AX %d）", name, code))
	}
	return nil
}
func (d *macAXDriver) postKey(ctx context.Context, pid int32, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	code, flags, err := macKeyCode(key)
	if err != nil {
		return err
	}
	for _, down := range []bool{true, false} {
		// 已发送 keyDown 后必须成对发送 keyUp，取消只发生在完整按键之间。
		event := d.api.keyEvent(0, code, down)
		if event == 0 {
			return failure(ErrProviderCrashed, "无法创建按键事件")
		}
		d.api.keyFlags(event, flags)
		d.api.keyPost(pid, event)
		d.api.cfRelease(event)
	}
	return nil
}

// 按 Unicode 字符投递到目标 PID，不借用全局剪贴板，并正确处理 UTF-16 代理对。
func (d *macAXDriver) typeKeyboard(ctx context.Context, pid int32, text string) error {
	for _, r := range text {
		if err := ctx.Err(); err != nil {
			return err
		}
		units := utf16.Encode([]rune{r})
		for _, down := range []bool{true, false} {
			event := d.api.keyEvent(0, 0, down)
			if event == 0 {
				return failure(ErrProviderCrashed, "无法创建文字输入事件")
			}
			d.api.keyUnicode(event, uintptr(len(units)), &units[0])
			d.api.keyPost(pid, event)
			d.api.cfRelease(event)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(15 * time.Millisecond):
		}
	}
	return nil
}
func (d *macAXDriver) Act(ctx context.Context, lease Lease, action ResolvedAction) (ActionResult, error) {
	if err := d.requireAX(); err != nil {
		return ActionResult{}, err
	}
	if err := ctx.Err(); err != nil {
		return ActionResult{}, err
	}
	defer macThread()()
	pid := int32(integer(lease.State["pid"]))
	if pid <= 0 || integer(lease.State["windowId"]) <= 0 {
		return ActionResult{}, failure(ErrTargetNotFound, "租约缺少有效 PID/windowId")
	}
	result := ActionResult{OK: true, Mode: "semantic", Data: map[string]any{"backend": "purego"}}
	if action.Type == ActionPressKey {
		err := d.postKey(ctx, pid, action.Key)
		result.Output = "已向目标应用发送按键 " + action.Key
		return result, err
	}
	element, release, err := d.element(ctx, lease, action)
	if err != nil {
		return ActionResult{}, err
	}
	defer release()
	if !d.attrBool(element, "AXEnabled", true) {
		return ActionResult{}, failure(ErrActionBlocked, "目标元素已禁用")
	}
	switch action.Type {
	case ActionClickElement:
		role := d.attrText(element, "AXRole")
		if role == "AXTextField" || role == "AXTextArea" {
			err = d.setFocus(element)
			result.Output = "已聚焦输入框"
		} else {
			name := "AXPress"
			if action.SnapshotElement != nil {
				for _, candidate := range []string{"AXPress", "AXShowDefaultUI", "AXOpen", "AXPick"} {
					if contains(action.SnapshotElement.Actions, candidate) {
						name = candidate
						break
					}
				}
			}
			err = d.perform(element, name)
			result.Output = "已执行 " + name
		}
	case ActionSecondary:
		err = d.perform(element, "AXShowMenu")
		result.Output = "已请求目标元素显示菜单"
	case ActionTypeText:
		if err = d.setFocus(element); err != nil {
			return ActionResult{}, err
		}
		var settable bool
		if d.api.axSettable(element, d.key("AXSelectedText"), &settable) == 0 && settable {
			text := d.api.cfStringCreate(0, action.Text, 0x08000100)
			code := d.api.axSet(element, d.key("AXSelectedText"), text)
			d.api.cfRelease(text)
			if code == 0 {
				result.Output = "已通过 AX 输入文字"
				break
			}
		}
		err = d.typeKeyboard(ctx, pid, action.Text)
		result.Output = "已通过 CGEvent 输入文字（AX fallback）"
	case ActionScroll:
		if err = d.setFocus(element); err != nil {
			return ActionResult{}, err
		}
		amount := action.Amount
		if amount <= 0 {
			amount = 3
		}
		for i := 0; i < amount; i++ {
			if err = d.postKey(ctx, pid, action.Direction); err != nil {
				break
			}
		}
		result.Output = fmt.Sprintf("已向目标元素发送 %d 次 %s 滚动按键", amount, action.Direction)
	default:
		return ActionResult{}, failure(ErrCapabilityUnsupported, "原生后端不支持该动作")
	}
	return result, err
}

// 独立进程组使取消可以清理 helper 派生的进程，避免只杀掉外层 CLI。
func configureProcess(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true} }
func killProcess(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
