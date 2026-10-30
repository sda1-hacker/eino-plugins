//go:build darwin

package computeruse

import (
	"context"
	"testing"
)

// 使用函数替身检查底层事件与 CF 生命周期，不发送真实系统事件。
func TestNativeKeyCancellationKeepsKeyPair(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var posted, released int
	var downs []bool
	d := &macAXDriver{api: &macSystemAPI{
		keyEvent:  func(_ uintptr, _ uint16, down bool) uintptr { downs = append(downs, down); return 1 },
		keyFlags:  func(uintptr, uint64) {},
		keyPost:   func(int32, uintptr) { posted++; cancel() },
		cfRelease: func(uintptr) { released++ },
	}}
	if err := d.postKey(ctx, 42, "return"); err != nil {
		t.Fatal(err)
	}
	if posted != 2 || released != 2 || len(downs) != 2 || !downs[0] || downs[1] {
		t.Fatal("取消发生在 keyDown 之后也必须发送 keyUp 并释放两个事件", posted, released, downs)
	}
	if err := d.postKey(ctx, 42, "return"); err != context.Canceled || posted != 2 {
		t.Fatal("已取消的下一次操作不能继续发送事件", err)
	}
}

func TestNativeCloseReleasesRetainedObjects(t *testing.T) {
	var released []uintptr
	d := &macAXDriver{api: &macSystemAPI{cfRelease: func(ref uintptr) { released = append(released, ref) }}, keys: map[string]uintptr{"AXTitle": 1}, elements: map[string]uintptr{"ax:1:0": 2}, pid: 42, window: 3}
	_ = d.Close()
	_ = d.Close()
	if len(released) != 2 || len(d.keys) != 0 || len(d.elements) != 0 || d.pid != 0 || d.window != 0 {
		t.Fatal("清理必须释放拥有的 CF 对象并且可以重复调用", released)
	}
}
