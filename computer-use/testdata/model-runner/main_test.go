package main

import (
	"testing"

	computeruse "github.com/sda1-hacker/eino-plugins/computer-use"
)

func TestVerifyStateUsesLatestObservation(t *testing.T) {
	r := &modelReport{Checks: map[string]bool{}}
	verifier := &auditedTool{report: r, code: "123456"}
	state := func(text, button string) *computeruse.ToolResult {
		return &computeruse.ToolResult{Details: map[string]any{"elements": []computeruse.Element{
			{Label: "Eino 验证输入框", Value: text}, {Label: button},
		}}}
	}
	verifier.verifyState(state("模型验收：123456", "Eino 验证按钮，已点击 1 次"))
	if !r.Checks["visual_code_entered_and_read_back"] || !r.Checks["button_effect_read_back"] {
		t.Fatal("未接受真实正确状态")
	}
	verifier.verifyState(state("后来被错误覆盖", "Eino 验证按钮，已点击 2 次"))
	if r.Checks["visual_code_entered_and_read_back"] || r.Checks["button_effect_read_back"] {
		t.Fatal("历史成功状态掩盖了最后一次错误观察")
	}
}
