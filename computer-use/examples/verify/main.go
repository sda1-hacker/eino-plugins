// verify 使用真实 Eino ToolsNode 和原生 Provider 验证独立测试窗口。
// 所有动作都有后续观察作为证据；系统权限不足时记录 blocked，不记作通过。
package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/schema"
	computeruse "github.com/sda1-hacker/eino-plugins/computer-use"
)

const fixtureAppID = "local.eino.computeruse.validation"

type stepResult struct {
	Name      string                `json:"name"`
	Passed    bool                  `json:"passed"`
	ErrorCode computeruse.ErrorCode `json:"errorCode,omitempty"`
	Error     string                `json:"error,omitempty"`
	Output    string                `json:"output,omitempty"`
}
type report struct {
	StartedAt time.Time    `json:"startedAt"`
	Passed    bool         `json:"passed"`
	Blocked   bool         `json:"blocked"`
	Reason    string       `json:"reason,omitempty"`
	Helper    string       `json:"helper"`
	Steps     []stepResult `json:"steps"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() (runErr error) {
	helper := flag.String("helper", "", "本次编译的 helper 路径")
	reportPath := flag.String("report", ".cache/validation/native-verification.json", "验证报告路径")
	requestPermissions := flag.Bool("request-permissions", false, "显式请求系统无障碍权限")
	flag.Parse()
	r := report{StartedAt: time.Now(), Helper: *helper}
	defer func() {
		if runErr != nil {
			r.Reason = runErr.Error()
		}
		_ = os.MkdirAll(filepath.Dir(*reportPath), 0755)
		b, _ := json.MarshalIndent(r, "", "  ")
		if err := os.WriteFile(*reportPath, b, 0600); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		fmt.Println(string(b))
	}()
	if *helper == "" {
		return fmt.Errorf("需要指定 -helper")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	settings := computeruse.DefaultSettings()
	settings.Enabled = true
	desktop, err := computeruse.NewComputerUseTool(ctx, &computeruse.Config{Settings: &settings, Providers: []computeruse.Provider{computeruse.NewMacOSProvider(&computeruse.MacOSConfig{Command: *helper, DisableCursor: true, SocketPath: filepath.Join(filepath.Dir(*reportPath), "native-helper.sock")})}, DefaultProviderID: "macos:cua", Model: &computeruse.ModelInfo{Input: []string{"image"}}, ConfirmApp: func(_ context.Context, request computeruse.ApprovalRequest) (computeruse.ApprovalDecision, error) {
		status := "denied"
		if request.Approval.AppID == fixtureAppID {
			status = "approved"
		}
		return computeruse.ApprovalDecision{Status: status, Scope: "session"}, nil
	}})
	if err != nil {
		return err
	}
	defer func() {
		if err := desktop.Cleanup(); err != nil && runErr == nil {
			runErr = err
			r.Passed = false
		}
	}()
	if *requestPermissions {
		status, err := desktop.Host().RequestPermissions(ctx, "macos:cua")
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "原生权限状态：%+v\n", status)
	}
	node, err := compose.NewToolNode(ctx, &compose.ToolsNodeConfig{Tools: []tool.BaseTool{desktop}, ExecuteSequentially: true})
	if err != nil {
		return err
	}
	callID := 0
	call := func(name string, p computeruse.Param) (*computeruse.ToolResult, *schema.Message, error) {
		callID++
		b, _ := json.Marshal(p)
		messages, err := node.Invoke(ctx, schema.AssistantMessage("", []schema.ToolCall{{ID: fmt.Sprint(callID), Type: "function", Function: schema.FunctionCall{Name: "computer", Arguments: string(b)}}}))
		if err != nil {
			r.Steps = append(r.Steps, stepResult{Name: name, Error: err.Error()})
			return nil, nil, err
		}
		if len(messages) != 1 {
			return nil, nil, fmt.Errorf("ToolsNode 没有返回单个工具消息")
		}
		m := messages[0]
		text := m.Content
		for _, part := range m.UserInputMultiContent {
			if part.Text != "" {
				text = part.Text
				break
			}
		}
		var result computeruse.ToolResult
		if err = json.Unmarshal([]byte(text), &result); err != nil {
			return nil, nil, err
		}
		step := stepResult{Name: name, Passed: result.Error == "", ErrorCode: result.ErrorCode, Error: result.Error}
		if p.Action != computeruse.ActionGetAppState {
			step.Output = result.Output
		}
		r.Steps = append(r.Steps, step)
		if result.Error != "" {
			r.Blocked = result.ErrorCode == computeruse.ErrOSPermissionDenied || result.ErrorCode == computeruse.ErrAppApprovalRequired
			return &result, m, fmt.Errorf("%s: %s", result.ErrorCode, result.Error)
		}
		// 保留每次真实观察的结构化结果与图片，方便人工复核解析和界面变化。
		if p.Action == computeruse.ActionGetAppState {
			stateJSON, e := json.MarshalIndent(result, "", "  ")
			if e != nil {
				return nil, nil, e
			}
			if e = os.WriteFile(filepath.Join(filepath.Dir(*reportPath), name+"-state.json"), stateJSON, 0600); e != nil {
				return nil, nil, e
			}
			for _, part := range m.UserInputMultiContent {
				if part.Image == nil || part.Image.Base64Data == nil {
					continue
				}
				raw, e := base64.StdEncoding.DecodeString(*part.Image.Base64Data)
				if e != nil {
					return nil, nil, e
				}
				_, format, e := image.DecodeConfig(bytes.NewReader(raw))
				if e != nil {
					return nil, nil, e
				}
				if e = os.WriteFile(filepath.Join(filepath.Dir(*reportPath), name+"."+format), raw, 0600); e != nil {
					return nil, nil, e
				}
			}
		}
		return &result, m, nil
	}
	if _, _, err = call("status", computeruse.Param{Action: computeruse.ActionStatus}); err != nil {
		return err
	}
	listed, _, err := call("list_apps", computeruse.Param{Action: computeruse.ActionListApps})
	if err != nil {
		return err
	}
	b, _ := json.Marshal(listed.Details["apps"])
	var apps []computeruse.App
	_ = json.Unmarshal(b, &apps)
	found := false
	for _, a := range apps {
		if a.AppID == fixtureAppID {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("独立验证窗口没有运行，请先启动 testdata/macos-fixture")
	}
	if _, _, err = call("start_fixture", computeruse.Param{Action: computeruse.ActionStart, AppID: fixtureAppID}); err != nil {
		return err
	}
	state, message, err := call("observe_fixture", computeruse.Param{Action: computeruse.ActionGetAppState})
	if err != nil {
		return err
	}
	hasImage := false
	for _, part := range message.UserInputMultiContent {
		if part.Image != nil && part.Image.Base64Data != nil {
			raw, e := base64.StdEncoding.DecodeString(*part.Image.Base64Data)
			if e != nil {
				return e
			}
			dimensions, format, e := image.DecodeConfig(bytes.NewReader(raw))
			if e != nil || dimensions.Width <= 0 || dimensions.Height <= 0 {
				return fmt.Errorf("实际截图无法解码: %v", e)
			}
			path := filepath.Join(filepath.Dir(*reportPath), "fixture-observation."+format)
			if e = os.WriteFile(path, raw, 0600); e != nil {
				return e
			}
			hasImage = true
		}
	}
	r.Steps = append(r.Steps, stepResult{Name: "decode_real_screenshot", Passed: hasImage})
	if !hasImage {
		return fmt.Errorf("截图未作为图片 part 通过 Eino")
	}
	getElements := func(result *computeruse.ToolResult) []computeruse.Element {
		b, _ := json.Marshal(result.Details["elements"])
		var elements []computeruse.Element
		_ = json.Unmarshal(b, &elements)
		return elements
	}
	find := func(elements []computeruse.Element, needle string) string {
		for _, e := range elements {
			if strings.Contains(e.Label, needle) {
				return e.ElementID
			}
		}
		return ""
	}
	// 动作成功只表示投递完成；轮询后续 AX 状态，验证应用实际处理了事件。
	hasText := func(result *computeruse.ToolResult, needle string) bool {
		if strings.Contains(fmt.Sprint(result.Details["accessibilityTree"]), needle) {
			return true
		}
		for _, e := range getElements(result) {
			if strings.Contains(e.Label, needle) || strings.Contains(fmt.Sprint(e.Value), needle) {
				return true
			}
		}
		return false
	}
	assertStep := func(name string, passed bool) error {
		r.Steps = append(r.Steps, stepResult{Name: name, Passed: passed})
		if !passed {
			return fmt.Errorf("真实界面断言失败：%s", name)
		}
		return nil
	}
	observeUntil := func(name string, predicate func(*computeruse.ToolResult) bool) (*computeruse.ToolResult, error) {
		for attempt := 0; attempt < 8; attempt++ {
			result, _, e := call(fmt.Sprintf("%s_%d", name, attempt+1), computeruse.Param{Action: computeruse.ActionGetAppState})
			if e != nil {
				return nil, e
			}
			if predicate(result) {
				return result, nil
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(150 * time.Millisecond):
			}
		}
		_ = assertStep("verify_"+name, false)
		return nil, fmt.Errorf("等待真实界面变化超时：%s", name)
	}
	inputID := find(getElements(state), "Eino 验证输入框")
	if inputID == "" {
		return fmt.Errorf("AX 树没有暴露测试输入框")
	}
	if _, _, err = call("click_input_to_focus", computeruse.Param{Action: computeruse.ActionClickElement, ElementID: inputID}); err != nil {
		return err
	}
	state, _, err = call("observe_focused_input", computeruse.Param{Action: computeruse.ActionGetAppState})
	if err != nil {
		return err
	}
	inputID = find(getElements(state), "Eino 验证输入框")
	text := "Eino 真实桌面验证：中文输入成功"
	if _, _, err = call("type_chinese", computeruse.Param{Action: computeruse.ActionTypeText, ElementID: inputID, Text: &text}); err != nil {
		return err
	}
	state, _, err = call("observe_typed_text", computeruse.Param{Action: computeruse.ActionGetAppState})
	if err != nil {
		return err
	}
	valueFound := false
	for _, e := range getElements(state) {
		if str := fmt.Sprint(e.Value); strings.Contains(str, text) {
			valueFound = true
		}
	}
	r.Steps = append(r.Steps, stepResult{Name: "verify_chinese_value", Passed: valueFound})
	if !valueFound {
		return fmt.Errorf("输入结果没有在下一次 AX 观察中读回")
	}
	if _, _, err = call("press_return", computeruse.Param{Action: computeruse.ActionPressKey, Key: "return"}); err != nil {
		return err
	}
	state, err = observeUntil("return_effect", func(s *computeruse.ToolResult) bool { return hasText(s, "Eino 按键确认：1 次") })
	if err != nil {
		return err
	}
	if err = assertStep("verify_return_effect", true); err != nil {
		return err
	}

	fallbackID := find(getElements(state), "Eino 键盘回退输入框")
	if fallbackID == "" {
		return fmt.Errorf("AX 树没有暴露键盘回退输入框")
	}
	fallbackText := "Eino 键盘回退中文成功"
	fallbackResult, _, err := call("type_chinese_keyboard_fallback", computeruse.Param{Action: computeruse.ActionTypeText, ElementID: fallbackID, Text: &fallbackText})
	if err != nil {
		return err
	}
	if err = assertStep("verify_keyboard_fallback_path", strings.Contains(fallbackResult.Output, "CGEvent")); err != nil {
		return err
	}
	state, err = observeUntil("keyboard_fallback_value", func(s *computeruse.ToolResult) bool { return hasText(s, fallbackText) })
	if err != nil {
		return err
	}
	if err = assertStep("verify_keyboard_fallback_value", true); err != nil {
		return err
	}
	if _, _, err = call("press_delete", computeruse.Param{Action: computeruse.ActionPressKey, Key: "delete"}); err != nil {
		return err
	}
	runes := []rune(fallbackText)
	deletedText := string(runes[:len(runes)-1])
	state, err = observeUntil("delete_effect", func(s *computeruse.ToolResult) bool {
		for _, e := range getElements(s) {
			if e.Label == "Eino 键盘回退输入框" && fmt.Sprint(e.Value) == deletedText {
				return true
			}
		}
		return false
	})
	if err != nil {
		return err
	}
	if err = assertStep("verify_delete_effect", true); err != nil {
		return err
	}
	buttonID := find(getElements(state), "Eino 验证按钮")
	if buttonID == "" {
		return fmt.Errorf("AX 树没有暴露测试按钮")
	}
	if _, _, err = call("click_button", computeruse.Param{Action: computeruse.ActionClickElement, ElementID: buttonID}); err != nil {
		return err
	}
	state, _, err = call("observe_clicked_button", computeruse.Param{Action: computeruse.ActionGetAppState})
	if err != nil {
		return err
	}
	clicked := find(getElements(state), "已点击 1 次") != ""
	r.Steps = append(r.Steps, stepResult{Name: "verify_button_effect", Passed: clicked})
	if !clicked {
		return fmt.Errorf("按钮点击没有在下一次观察中产生可验证效果")
	}

	offsetPattern := regexp.MustCompile(`Eino 滚动偏移：(\d+)`)
	scrollOffset := func(s *computeruse.ToolResult) int {
		if match := offsetPattern.FindStringSubmatch(fmt.Sprint(s.Details["accessibilityTree"])); len(match) == 2 {
			n, _ := strconv.Atoi(match[1])
			return n
		}
		for _, e := range getElements(s) {
			if match := offsetPattern.FindStringSubmatch(e.Label + " " + fmt.Sprint(e.Value)); len(match) == 2 {
				n, _ := strconv.Atoi(match[1])
				return n
			}
		}
		return -1
	}
	scrollID := find(getElements(state), "Eino 验证滚动内容")
	if scrollID == "" {
		return fmt.Errorf("AX 树没有暴露原生滚动内容")
	}
	initialOffset := scrollOffset(state)
	if _, _, err = call("scroll_down", computeruse.Param{Action: computeruse.ActionScroll, ElementID: scrollID, Direction: "down", Amount: 20}); err != nil {
		return err
	}
	state, err = observeUntil("scroll_down_effect", func(s *computeruse.ToolResult) bool { return scrollOffset(s) > initialOffset })
	if err != nil {
		return err
	}
	downOffset := scrollOffset(state)
	if err = assertStep("verify_scroll_down_effect", initialOffset >= 0 && downOffset > initialOffset); err != nil {
		return err
	}
	scrollID = find(getElements(state), "Eino 验证滚动内容")
	if _, _, err = call("scroll_up", computeruse.Param{Action: computeruse.ActionScroll, ElementID: scrollID, Direction: "up", Amount: 20}); err != nil {
		return err
	}
	state, err = observeUntil("scroll_up_effect", func(s *computeruse.ToolResult) bool { return scrollOffset(s) >= 0 && scrollOffset(s) < downOffset })
	if err != nil {
		return err
	}
	if err = assertStep("verify_scroll_up_effect", true); err != nil {
		return err
	}

	secondaryID := find(getElements(state), "Eino 次级动作按钮")
	if secondaryID == "" {
		return fmt.Errorf("AX 树没有暴露次级动作按钮")
	}
	if _, _, err = call("perform_secondary_action", computeruse.Param{Action: computeruse.ActionSecondary, ElementID: secondaryID}); err != nil {
		return err
	}
	state, err = observeUntil("secondary_menu_open", func(s *computeruse.ToolResult) bool { return hasText(s, "Eino 次级动作：1 次，菜单已打开") })
	if err != nil {
		return err
	}
	if err = assertStep("verify_secondary_menu_open", true); err != nil {
		return err
	}
	if _, _, err = call("press_escape", computeruse.Param{Action: computeruse.ActionPressKey, Key: "escape"}); err != nil {
		return err
	}
	if _, err = observeUntil("secondary_menu_closed", func(s *computeruse.ToolResult) bool { return hasText(s, "Eino 次级动作：1 次，菜单已关闭") }); err != nil {
		return err
	}
	if err = assertStep("verify_escape_closes_menu", true); err != nil {
		return err
	}
	if _, _, err = call("stop", computeruse.Param{Action: computeruse.ActionStop}); err != nil {
		return err
	}
	r.Passed = true
	return nil
}
