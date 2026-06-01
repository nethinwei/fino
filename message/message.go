// Package message defines the data types exchanged between models, tools, and
// the Runner in the fino Agent SDK.
package message

import "encoding/json"

// Role identifies the sender of a Message.
type Role string

const (
	// RoleSystem identifies a system instruction message.
	RoleSystem Role = "system"
	// RoleUser identifies a user message.
	RoleUser Role = "user"
	// RoleAssistant identifies an assistant (model) message.
	RoleAssistant Role = "assistant"
	// RoleTool identifies a tool result message.
	RoleTool Role = "tool"
)

// BlockType identifies the kind of content in a Block.
type BlockType string

const (
	// TypeText is a plain text content block.
	TypeText BlockType = "text"
	// TypeToolUse is a model request to invoke a tool.
	TypeToolUse BlockType = "tool_use"
	// TypeToolResult is the result of a tool invocation.
	TypeToolResult BlockType = "tool_result"
	// TypeThinking is a model reasoning block.
	TypeThinking BlockType = "thinking"
)

// Message is the universal data type exchanged between models, tools, and the
// Runner. It uses a single content-block model with a Role discriminator.
type Message struct {
	Role    Role    `json:"role"`
	Content []Block `json:"content,omitempty"`
}

// Block is a flat discriminated union of content types. The Type field
// determines which other fields are populated. JSON serialization produces a
// flat shape (e.g. {"type":"text","text":"hello"}) rather than nested objects.
type Block struct {
	Type      BlockType       `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   []Block         `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`
}

// ToolUse extracts the fields relevant to a tool_use block.
type ToolUse struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// SystemText returns a system Message containing a single text block.
func SystemText(text string) Message {
	return Message{Role: RoleSystem, Content: []Block{NewText(text)}}
}

// UserText returns a user Message containing a single text block.
func UserText(text string) Message {
	return Message{Role: RoleUser, Content: []Block{NewText(text)}}
}

// Assistant returns an assistant Message with the given content blocks.
func Assistant(blocks ...Block) Message {
	return Message{Role: RoleAssistant, Content: blocks}
}

// ToolResults returns a tool Message containing one or more tool_result
// blocks. This produces a single RoleTool message with batched results, which
// aligns with Anthropic and OpenAI tool-calling protocols.
func ToolResults(results ...Block) Message {
	return Message{Role: RoleTool, Content: results}
}

// NewText returns a text content block.
func NewText(text string) Block {
	return Block{Type: TypeText, Text: text}
}

// NewThinking returns a thinking content block.
func NewThinking(text string) Block {
	return Block{Type: TypeThinking, Text: text}
}

// NewToolUse returns a tool_use content block.
func NewToolUse(id, name string, input json.RawMessage) Block {
	return Block{Type: TypeToolUse, ID: id, Name: name, Input: input}
}

// NewToolResult returns a tool_result content block. The content parameter
// holds the result as one or more nested blocks.
func NewToolResult(toolUseID, name string, content []Block, isError bool) Block {
	return Block{Type: TypeToolResult, ToolUseID: toolUseID, Name: name, Content: content, IsError: isError}
}

// Text returns the concatenated text of all text-type blocks in the Message.
func (m Message) Text() string {
	out := ""
	for _, block := range m.Content {
		if block.Type == TypeText {
			out += block.Text
		}
	}
	return out
}

// ToolUses returns all tool_use blocks extracted as ToolUse values.
func (m Message) ToolUses() []ToolUse {
	out := []ToolUse{}
	for _, block := range m.Content {
		if block.Type == TypeToolUse {
			out = append(out, ToolUse{ID: block.ID, Name: block.Name, Input: block.Input})
		}
	}
	return out
}

// HasSystem reports whether any message in the slice has RoleSystem.
func HasSystem(messages []Message) bool {
	for _, msg := range messages {
		if msg.Role == RoleSystem {
			return true
		}
	}
	return false
}
