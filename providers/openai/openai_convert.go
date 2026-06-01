package openai

import (
	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/tool"
)

// toOpenAIMessages converts fino messages into OpenAI chat messages. Assistant
// tool_use blocks become tool_calls; a RoleTool message expands into one chat
// message per tool_result block, keyed by tool_call_id, as the API requires.
func toOpenAIMessages(msgs []message.Message) []chatMessage {
	out := make([]chatMessage, 0, len(msgs))
	for _, msg := range msgs {
		switch msg.Role {
		case message.RoleTool:
			out = append(out, toolMessages(msg)...)
		case message.RoleAssistant:
			out = append(out, assistantMessage(msg))
		default:
			out = append(out, chatMessage{Role: string(msg.Role), Content: msg.Text()})
		}
	}
	return out
}

// toolMessages expands a RoleTool message into one chat message per tool_result.
func toolMessages(msg message.Message) []chatMessage {
	out := make([]chatMessage, 0, len(msg.Content))
	for _, b := range msg.Content {
		if b.Type == message.TypeToolResult {
			out = append(out, chatMessage{Role: "tool", ToolCallID: b.ToolUseID, Content: blocksText(b.Content)})
		}
	}
	return out
}

// assistantMessage builds an assistant chat message, attaching any tool_use
// blocks as tool_calls alongside the text content.
func assistantMessage(msg message.Message) chatMessage {
	cm := chatMessage{Role: "assistant", Content: msg.Text()}
	for _, b := range msg.Content {
		if b.Type != message.TypeToolUse {
			continue
		}
		tc := chatToolCall{ID: b.ID, Type: "function"}
		tc.Function.Name = b.Name
		tc.Function.Arguments = string(b.Input)
		cm.ToolCalls = append(cm.ToolCalls, tc)
	}
	return cm
}

// blocksText concatenates the text of all text-type blocks.
func blocksText(blocks []message.Block) string {
	out := ""
	for _, b := range blocks {
		if b.Type == message.TypeText {
			out += b.Text
		}
	}
	return out
}

// toOpenAITools converts tool infos into OpenAI function tool definitions.
func toOpenAITools(tools []tool.Info) []chatTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]chatTool, 0, len(tools))
	for _, t := range tools {
		var ct chatTool
		ct.Type = "function"
		ct.Function.Name = t.Name
		ct.Function.Description = t.Description
		ct.Function.Parameters = t.InputSchema
		out = append(out, ct)
	}
	return out
}
