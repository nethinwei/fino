package openai

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/tool"
)

// toOpenAIMessages converts fino messages into OpenAI chat messages. Assistant
// tool_use blocks become tool_calls; a RoleTool message expands into one chat
// message per tool_result block, keyed by tool_call_id, as the API requires.
// Multimodal blocks in user/assistant content become OpenAI content parts.
func toOpenAIMessages(msgs []message.Message) []chatMessage {
	out := make([]chatMessage, 0, len(msgs))
	for _, msg := range msgs {
		switch msg.Role {
		case message.RoleTool:
			out = append(out, toolMessages(msg)...)
		case message.RoleAssistant:
			out = append(out, assistantMessage(msg))
		default:
			out = append(out, chatMessage{Role: string(msg.Role), Content: buildContent(msg.Content)})
		}
	}
	return out
}

// toolMessages expands a RoleTool message into one chat message per tool_result.
// The tool role accepts only a string content, so multimodal tool results
// degrade to a text placeholder.
func toolMessages(msg message.Message) []chatMessage {
	out := make([]chatMessage, 0, len(msg.Content))
	for _, b := range msg.Content {
		if b.Type == message.TypeToolResult {
			out = append(out, chatMessage{
				Role:       "tool",
				ToolCallID: b.ToolUseID,
				Content:    textContent(blocksTextOrPlaceholder(b.Content)),
			})
		}
	}
	return out
}

// assistantMessage builds an assistant chat message, attaching any tool_use
// blocks as tool_calls alongside the text/multimodal content.
func assistantMessage(msg message.Message) chatMessage {
	contentBlocks := make([]message.Block, 0, len(msg.Content))
	for _, b := range msg.Content {
		if b.Type != message.TypeToolUse {
			contentBlocks = append(contentBlocks, b)
		}
	}
	cm := chatMessage{Role: "assistant", Content: buildContent(contentBlocks)}
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

// buildContent marshals a slice of blocks into OpenAI's content field: a JSON
// string for text-only content, or an array of content parts for multimodal
// content. It returns nil for empty content so the field is omitted.
func buildContent(blocks []message.Block) json.RawMessage {
	if len(blocks) == 0 {
		return nil
	}
	if onlyText(blocks) {
		return textContent(blocksText(blocks))
	}
	parts := make([]openaiContentPart, 0, len(blocks))
	for _, b := range blocks {
		parts = append(parts, blockToOpenAIPart(b))
	}
	data, err := json.Marshal(parts)
	if err != nil {
		return textContent(blocksText(blocks))
	}
	return data
}

// textContent quotes a string into a JSON RawMessage suitable for the content
// field. It returns nil for an empty string so the field is omitted via
// omitempty, preserving the pre-multimodal behavior of omitting empty content.
func textContent(s string) json.RawMessage {
	if s == "" {
		return nil
	}
	return json.RawMessage(strconv.Quote(s))
}

func onlyText(blocks []message.Block) bool {
	for _, b := range blocks {
		if b.Type != message.TypeText {
			return false
		}
	}
	return true
}

// blockToOpenAIPart converts a fino block into an OpenAI content part. Image
// and audio map to image_url/input_audio; video and file have no standard
// content part and degrade to a text placeholder.
func blockToOpenAIPart(b message.Block) openaiContentPart {
	switch b.Type {
	case message.TypeText:
		return openaiContentPart{Type: "text", Text: b.Text}
	case message.TypeImage:
		if u := imageURLOpt(b); u != nil {
			return openaiContentPart{Type: "image_url", ImageURL: u}
		}
		return openaiContentPart{Type: "text", Text: mediaPlaceholder(b)}
	case message.TypeAudio:
		if a := inputAudioOpt(b); a != nil {
			return openaiContentPart{Type: "input_audio", InputAudio: a}
		}
		return openaiContentPart{Type: "text", Text: mediaPlaceholder(b)}
	default:
		return openaiContentPart{Type: "text", Text: mediaPlaceholder(b)}
	}
}

func imageURLOpt(b message.Block) *openaiImageURL {
	u := &openaiImageURL{Detail: b.Detail}
	switch b.SourceType {
	case message.SourceBase64:
		u.URL = "data:" + b.MediaType + ";base64," + b.Data
	case message.SourceURL:
		u.URL = b.URL
	default:
		return nil
	}
	if u.URL == "" {
		return nil
	}
	return u
}

func inputAudioOpt(b message.Block) *openaiInputAudio {
	if b.SourceType != message.SourceBase64 {
		return nil
	}
	return &openaiInputAudio{Data: b.Data, Format: audioFormat(b.MediaType)}
}

// audioFormat derives the OpenAI audio format from a media type ("audio/wav"
// -> "wav").
func audioFormat(mediaType string) string {
	if i := strings.LastIndex(mediaType, "/"); i >= 0 {
		return mediaType[i+1:]
	}
	return mediaType
}

func mediaPlaceholder(b message.Block) string {
	return "[" + string(b.Type) + ": " + b.MediaType + "]"
}

// respContentToBlocks parses an OpenAI response content field (string or array)
// into fino blocks.
func respContentToBlocks(raw json.RawMessage) []message.Block {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if s == "" {
			return nil
		}
		return []message.Block{message.NewText(s)}
	}
	var parts []openaiContentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return nil
	}
	out := make([]message.Block, 0, len(parts))
	for _, p := range parts {
		if b, ok := partToBlock(p); ok {
			out = append(out, b)
		}
	}
	return out
}

func partToBlock(p openaiContentPart) (message.Block, bool) {
	switch p.Type {
	case "text":
		return message.NewText(p.Text), true
	case "image_url":
		if p.ImageURL == nil {
			return message.Block{}, false
		}
		return imageURLToBlock(p.ImageURL), true
	}
	return message.Block{}, false
}

func imageURLToBlock(u *openaiImageURL) message.Block {
	if mediaType, data, ok := parseDataURL(u.URL); ok {
		return message.NewImage(mediaType, message.WithBase64(data), message.WithDetail(u.Detail))
	}
	return message.NewImage("image/png", message.WithURL(u.URL), message.WithDetail(u.Detail))
}

// parseDataURL splits "data:image/png;base64,AAA" into media type and data.
func parseDataURL(s string) (mediaType, data string, ok bool) {
	const prefix = "data:"
	if !strings.HasPrefix(s, prefix) {
		return "", "", false
	}
	rest := s[len(prefix):]
	comma := strings.Index(rest, ",")
	if comma < 0 {
		return "", "", false
	}
	head := rest[:comma]
	data = rest[comma+1:]
	mediaType = strings.TrimSuffix(head, ";base64")
	return mediaType, data, true
}

func audioRespToBlock(a *respAudio) message.Block {
	format := a.Format
	if format == "" {
		format = "wav"
	}
	return message.NewAudio("audio/"+format, message.WithBase64(a.Data))
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

// blocksTextOrPlaceholder concatenates text and degrades multimodal blocks to
// a placeholder, for contexts that only accept a string (the tool role).
func blocksTextOrPlaceholder(blocks []message.Block) string {
	out := ""
	for _, b := range blocks {
		switch b.Type {
		case message.TypeText:
			out += b.Text
		default:
			out += mediaPlaceholder(b)
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
