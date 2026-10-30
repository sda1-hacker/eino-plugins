package computeruse

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestToolImagesPreserveBatchOrderAndOriginalMessages(t *testing.T) {
	image := &schema.MessageInputImage{MessagePartCommon: schema.MessagePartCommon{MIMEType: "image/png"}}
	first := &schema.Message{Role: schema.Tool, ToolCallID: "observe", UserInputMultiContent: []schema.MessageInputPart{
		{Type: schema.ChatMessagePartTypeText, Text: "snapshot-1"},
		{Type: schema.ChatMessagePartTypeImageURL, Image: image},
	}}
	second := schema.ToolMessage("第二个工具回复", "other")
	input := []*schema.Message{schema.AssistantMessage("", nil), first, second}
	output := ToolImagesAsUserMessages(context.Background(), input)
	if len(output) != 4 || output[1].ToolCallID != "observe" || output[1].Content != "snapshot-1" || output[2].ToolCallID != "other" {
		t.Fatalf("工具调用与回复顺序被破坏：%+v", output)
	}
	if output[3].Role != schema.User || output[3].UserInputMultiContent[1].Image != image {
		t.Fatal("截图未在同批工具回复之后交给模型")
	}
	if first.Content != "" || len(first.UserInputMultiContent) != 2 {
		t.Fatal("原始消息被修改，重复模型调用可能丢失图片")
	}
	if again := ToolImagesAsUserMessages(context.Background(), output); len(again) != len(output) {
		t.Fatal("重复处理增加了图片消息")
	}
}
