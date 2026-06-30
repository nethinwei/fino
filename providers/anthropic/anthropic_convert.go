package anthropic

import (
	"encoding/json"
	"strconv"
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
			out = append(out, msgMessage{Role: "user", Content: userBlocks(msg)})
		}
	}
	return system.String(), out
}

// toolResultBlocks converts a RoleTool message's tool_result blocks. Content is
// sent as a plain JSON string when only text is present, or as an array of
// blocks when the result carries multimodal content (e.g. a screenshot).
func toolResultBlocks(msg message.Message) []reqBlock {
	out := make([]reqBlock, 0, len(msg.Content))
	for _, b := range msg.Content {
		if b.Type == message.TypeToolResult {
			out = append(out, reqBlock{
				Type:      "tool_result",
				ToolUseID: b.ToolUseID,
				Content:   toolResultContent(b.Content),
				IsError:   b.IsError,
			})
		}
	}
	return out
}

// toolResultContent marshals a tool_result's nested blocks to Anthropic's
// content field: a plain JSON string when only text is present, or an array of
// blocks when multimodal content is present. An empty text result returns nil
// so the field is omitted via omitempty, matching the pre-multimodal behavior.
func toolResultContent(blocks []message.Block) json.RawMessage {
	if onlyText(blocks) {
		s := blocksText(blocks)
		if s == "" {
			return nil
		}
		return json.RawMessage(strconv.Quote(s))
	}
	parts := make([]reqBlock, 0, len(blocks))
	for _, b := range blocks {
		parts = append(parts, blockToReqBlock(b))
	}
	data, err := json.Marshal(parts)
	if err != nil {
		return json.RawMessage(strconv.Quote(blocksText(blocks)))
	}
	return data
}

func onlyText(blocks []message.Block) bool {
	for _, b := range blocks {
		if b.Type != message.TypeText {
			return false
		}
	}
	return true
}

// assistantBlocks converts an assistant message's text, tool_use, and
// multimodal blocks. Thinking blocks are omitted: replaying them requires a
// provider signature the core does not carry, and the Messages API rejects
// unsigned thinking blocks.
func assistantBlocks(msg message.Message) []reqBlock {
	out := make([]reqBlock, 0, len(msg.Content))
	for _, b := range msg.Content {
		switch b.Type {
		case message.TypeText:
			out = append(out, reqBlock{Type: "text", Text: b.Text})
		case message.TypeToolUse:
			out = append(out, reqBlock{Type: "tool_use", ID: b.ID, Name: b.Name, Input: b.Input})
		case message.TypeImage, message.TypeAudio, message.TypeVideo, message.TypeFile:
			out = append(out, finoMediaBlock(b))
		}
	}
	return out
}

// userBlocks converts a user message's blocks, including multimodal content.
func userBlocks(msg message.Message) []reqBlock {
	if len(msg.Content) == 0 {
		return []reqBlock{{Type: "text", Text: ""}}
	}
	out := make([]reqBlock, 0, len(msg.Content))
	for _, b := range msg.Content {
		out = append(out, blockToReqBlock(b))
	}
	return out
}

// blockToReqBlock converts any fino block into its Anthropic reqBlock form.
func blockToReqBlock(b message.Block) reqBlock {
	switch b.Type {
	case message.TypeText:
		return reqBlock{Type: "text", Text: b.Text}
	case message.TypeImage, message.TypeAudio, message.TypeVideo, message.TypeFile:
		return finoMediaBlock(b)
	default:
		return reqBlock{Type: string(b.Type), Text: b.Text}
	}
}

// finoMediaBlock folds a fino multimodal block into an Anthropic reqBlock with
// a nested source object.
func finoMediaBlock(b message.Block) reqBlock {
	return reqBlock{Type: anthropicType(b.Type), Source: sourceFromBlock(b)}
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
