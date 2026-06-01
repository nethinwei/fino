// Package hooks defines lifecycle hooks for observing and extending model calls
// and tool executions in the fino Agent SDK. Hooks are for observability only;
// they cannot veto calls. Use policy.Policy for authorization.
package hooks

import (
	"context"
	"encoding/json"

	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/tool"
)

// Hooks holds optional callback functions for the Runner lifecycle. All fields
// are nil-safe; the Runner skips nil callbacks.
type Hooks struct {
	// BeforeModel is called before each model invocation. It can return a
	// modified context.
	BeforeModel func(context.Context, ModelCall) context.Context
	// AfterModel is called after the model returns a response.
	AfterModel func(context.Context, ModelResult)
	// BeforeTool is called before each tool execution. It can return a
	// modified context.
	BeforeTool func(context.Context, ToolCall) context.Context
	// AfterTool is called after a tool execution completes.
	AfterTool func(context.Context, ToolResult)
	// OnError is called once for each terminal error before the run ends.
	OnError func(context.Context, error)
}

// ModelCall describes an upcoming model invocation.
type ModelCall struct {
	AgentName string
	ModeName  string
	Messages  []message.Message
	Tools     []tool.Info
}

// ModelResult describes a completed model response.
type ModelResult struct {
	AgentName string
	ModeName  string
	Message   *message.Message
}

// ToolCall describes an upcoming tool execution.
type ToolCall struct {
	AgentName string
	ModeName  string
	Tool      tool.Info
	Input     json.RawMessage
}

// ToolResult describes a completed tool execution.
type ToolResult struct {
	AgentName string
	ModeName  string
	Tool      tool.Info
	Result    tool.Result
}
