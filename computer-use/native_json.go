package computeruse

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// 原生协议使用 UseNumber，防止窗口句柄被浮点转换损失精度。
func decodeNative(data string) (any, error) {
	var v any
	d := json.NewDecoder(strings.NewReader(data))
	d.UseNumber()
	err := d.Decode(&v)
	return v, err
}
func object(v any) map[string]any { m, _ := v.(map[string]any); return m }
func array(v any) []any           { a, _ := v.([]any); return a }
func str(v any) string {
	if v == nil {
		return ""
	}
	switch x := v.(type) {
	case string:
		return x
	case json.Number:
		return x.String()
	default:
		return fmt.Sprint(v)
	}
}
func number(v any) float64 { n, _ := strconv.ParseFloat(str(v), 64); return n }
func integer(v any) int    { return int(number(v)) }
func boolean(v any) bool   { b, _ := v.(bool); return b }
func boolPointer(v any) *bool {
	b, ok := v.(bool)
	if !ok {
		return nil
	}
	return &b
}
func first(values ...any) any {
	for _, v := range values {
		if v == nil {
			continue
		}
		if text, ok := v.(string); ok && text == "" {
			continue
		}
		return v
	}
	return nil
}
func firstString(values ...any) string { return str(first(values...)) }
func stringsOf(v any) []string {
	var out []string
	for _, x := range array(v) {
		out = append(out, str(x))
	}
	return out
}
func boundsOf(v any) *Bounds {
	m := object(v)
	if len(m) == 0 {
		return nil
	}
	return &Bounds{X: number(m["x"]), Y: number(m["y"]), Width: number(m["width"]), Height: number(m["height"])}
}
func windowsOf(v any) []Window {
	out := []Window{}
	for _, x := range array(v) {
		m := object(x)
		id := firstString(m["window_id"], m["windowId"], m["id"], m["nativeWindowHandle"])
		if id == "" {
			continue
		}
		out = append(out, Window{
			WindowID:       id,
			Title:          firstString(m["title"], m["name"]),
			Bounds:         boundsOf(m["bounds"]),
			IsOnScreen:     boolPointer(first(m["is_on_screen"], m["isOnScreen"])),
			OnCurrentSpace: boolPointer(first(m["on_current_space"], m["onCurrentSpace"])),
		})
	}
	return out
}
func appsOf(v any) []App {
	items := array(v)
	if items == nil {
		m := object(v)
		items = array(first(m["apps"], m["items"]))
	}
	out := []App{}
	for _, x := range items {
		m := object(x)
		pid := integer(first(m["pid"], m["process_id"], m["processId"]))
		id := firstString(m["bundle_id"], m["bundleId"], m["appId"], m["id"])
		if id == "" && pid > 0 {
			id = fmt.Sprintf("pid:%d", pid)
		}
		if id == "" {
			continue
		}
		out = append(out, App{
			AppID:        id,
			Name:         firstString(m["name"], m["localized_name"], m["displayName"], id),
			PID:          pid,
			Active:       boolean(first(m["active"], m["is_active"])),
			Windows:      windowsOf(m["windows"]),
			ProviderData: map[string]any{"bundleId": id, "pid": pid},
		})
	}
	return out
}
