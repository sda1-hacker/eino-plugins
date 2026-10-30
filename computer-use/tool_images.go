package computeruse

import (
	"context"
	"strings"

	"github.com/cloudwego/eino/schema"
)

// ToolImagesAsUserMessages 适配只允许 tool 消息携带文本的聊天接口。
// 保留工具调用 ID 和文本结果，把截图放到同一批工具回复之后的 user 图片消息中。
// 原始消息保持不变，工具自身仍使用 Eino 原生多模态返回接口。
func ToolImagesAsUserMessages(_ context.Context, input []*schema.Message) []*schema.Message {
	output := make([]*schema.Message, 0, len(input)+1)
	var images []schema.MessageInputPart
	flush := func() {
		if len(images) == 0 {
			return
		}
		parts := []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeText, Text: "以下截图来自前面 computer 工具的观察结果，请结合对应 snapshotId 和元素编号判断。"}}
		parts = append(parts, images...)
		output = append(output, &schema.Message{Role: schema.User, UserInputMultiContent: parts})
		images = nil
	}
	for _, message := range input {
		if message.Role != schema.Tool {
			flush()
			output = append(output, message)
			continue
		}
		if len(message.UserInputMultiContent) == 0 {
			output = append(output, message)
			continue
		}
		var texts []string
		if message.Content != "" {
			texts = append(texts, message.Content)
		}
		var remaining []schema.MessageInputPart
		for _, part := range message.UserInputMultiContent {
			switch {
			case part.Image != nil:
				images = append(images, part)
			case part.Type == schema.ChatMessagePartTypeText:
				texts = append(texts, part.Text)
			default:
				remaining = append(remaining, part)
			}
		}
		copy := *message
		copy.Content = strings.Join(texts, "\n")
		copy.UserInputMultiContent = remaining
		output = append(output, &copy)
	}
	flush()
	return output
}
