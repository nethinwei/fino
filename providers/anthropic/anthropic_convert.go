package anthropic

import (
	"strings"

	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/tool"
)

// toAnthropicMessages splits fino messages into the top-level system string and
// the messages array. System messages are concatenated into system (Anthropic
// has no system role). A RoleTool message becomes a user message carrying
// tool_result blocks, as the Messages API requires.
func toAnthropicMessages(msgs []message.Message) (string, []msgMessage) {
	var system strings.Builder
	out := make([]msgMessage, 0, len(msgs))
	for _, msg := range msgs {
		switch msg.Role {
		case message.RoleSystem:
			system.WriteString(msg.Text())
		case message.RoleTool:
			out = append(out, msgMessage{Role: "user", Content: toolResultBlocks(msg)})
		case message.RoleAssistant:
			out = append(out, msgMessage{Role: "assistant", Content: assistantBlocks(msg)})
		default:
			out = append(out, msgMessage{Role: "user", Content: []reqBlock{{Type: "text", Text: msg.Text()}}})
		}
	}
	return system.String(), out
}

// toolResultBlocks converts a RoleTool message's tool_result blocks. Content is
// sent as a plain string, which the Messages API accepts.
func toolResultBlocks(msg message.Message) []reqBlock {
	out := make([]reqBlock, 0, len(msg.Content))
	for _, b := range msg.Content {
		if b.Type == message.TypeToolResult {
			out = append(out, reqBlock{
				Type:      "tool_result",
				ToolUseID: b.ToolUseID,
				Content:   blocksText(b.Content),
				IsError:   b.IsError,
			})
		}
	}
	return out
}

// assistantBlocks converts an assistant message's text and tool_use blocks.
// Thinking blocks are omitted: replaying them requires a provider signature the
// core does not carry, and the Messages API rejects unsigned thinking blocks.
func assistantBlocks(msg message.Message) []reqBlock {
	out := make([]reqBlock, 0, len(msg.Content))
	for _, b := range msg.Content {
		switch b.Type {
		case message.TypeText:
			out = append(out, reqBlock{Type: "text", Text: b.Text})
		case message.TypeToolUse:
			out = append(out, reqBlock{Type: "tool_use", ID: b.ID, Name: b.Name, Input: b.Input})
		}
	}
	return out
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

// toAnthropicTools converts tool infos into Anthropic tool definitions.
func toAnthropicTools(tools []tool.Info) []msgTool {
	if len(tools) == 0 {
		return nil
	}
	out := make([]msgTool, 0, len(tools))
	for _, t := range tools {
		out = append(out, msgTool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema})
	}
	return out
}
