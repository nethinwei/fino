# fino Core Agent SDK Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the smallest high-quality Go Agent SDK core that implements the ReAct feedback loop with maximum extension points and minimum ownership of user application concerns.

**Architecture:** The core contains `message`, `tool`, `model`, `agent`, `policy`, `hooks`, and `runner`. The SDK owns messages, tools, model interface, modes, policies, hooks, and the ReAct loop; users own orchestration, persistence, RAG, MCP, permissions semantics, provider clients, and deployment.

**Tech Stack:** Go 1.23+, standard library only in core, `go test ./...`, public constructors use required parameters plus `opts ...Option`.

---

## Hard Constraints

- No graph package, RAG package, MCP package, session package, provider client implementation, filesystem tool, bash tool, or hosted tool in core.
- No fixed permission fields such as `AllowWrite` or `AllowNetwork`; only `policy.Policy`.
- No global mutable state.
- No hidden session. `runner.Run` and `runner.Stream` receive explicit input.
- No nested content block JSON like `{"text":{"text":"hello"}}`; use flat discriminated union blocks.
- No handoff string signals. Handoff is recognized by `interface{ TargetAgent() *agent.Agent }`.
- No parallel tool execution in v1. Tool calls run serially in model order.
- No complex checkpoint. Context cancellation and policy denial are explicit errors/events.
- Use typed errors for branchable failures.
- Do not commit unless explicitly requested.

## Files

Create only these implementation files and tests:

```text
go.mod
doc.go
message/message.go
message/message_test.go
tool/tool.go
tool/tool_test.go
model/model.go
model/model_test.go
agent/agent.go
agent/agent_test.go
policy/policy.go
policy/policy_test.go
hooks/hooks.go
runner/runner.go
runner/runner_test.go
agent/example_test.go
```

---

## Task 1: Module Skeleton

**Files:**
- Create: `go.mod`
- Create: `doc.go`

- [ ] Write `go.mod`:

```go
module github.com/cloudwego/fino

go 1.23
```

- [ ] Write `doc.go`:

```go
// Package fino provides minimal primitives for building LLM agents.
//
// The core SDK implements the ReAct feedback loop and leaves orchestration,
// persistence, permissions semantics, RAG, tools, provider clients, and
// deployment to users.
package fino
```

- [ ] Run `go test ./...`.

Expected: PASS.

---

## Task 2: Message Package

**Files:**
- Create: `message/message.go`
- Create: `message/message_test.go`

### Requirements

- Single message type.
- Flat `Block` discriminated union.
- Supported block types: `text`, `tool_use`, `tool_result`, `thinking`.
- `tool_result` blocks store nested `Content []Block`.
- Helpers produce provider-friendly internal JSON without nested text objects.

- [ ] Write `message/message_test.go`:

```go
package message

import (
    "encoding/json"
    "strings"
    "testing"
)

func TestTextMessageHelpers(t *testing.T) {
    msg := UserText("hello")
    if msg.Role != RoleUser {
        t.Fatalf("role = %q", msg.Role)
    }
    if got := msg.Text(); got != "hello" {
        t.Fatalf("Text() = %q", got)
    }
}

func TestFlatJSONShape(t *testing.T) {
    data, err := json.Marshal(NewText("hello"))
    if err != nil {
        t.Fatalf("Marshal error: %v", err)
    }
    got := string(data)
    if !strings.Contains(got, `"type":"text"`) || !strings.Contains(got, `"text":"hello"`) {
        t.Fatalf("unexpected json: %s", got)
    }
    if strings.Contains(got, `"text":{"text"`) {
        t.Fatalf("nested text object is not allowed: %s", got)
    }
}

func TestToolUses(t *testing.T) {
    msg := Assistant(NewToolUse("call_1", "search", json.RawMessage(`{"query":"go"}`)))
    calls := msg.ToolUses()
    if len(calls) != 1 || calls[0].Name != "search" {
        t.Fatalf("calls = %#v", calls)
    }
}

func TestToolResultsMessageBatchesBlocks(t *testing.T) {
    msg := ToolResults(
        NewToolResult("call_1", "search", []Block{NewText("one")}, false),
        NewToolResult("call_2", "read", []Block{NewText("two")}, false),
    )
    if msg.Role != RoleTool {
        t.Fatalf("role = %q", msg.Role)
    }
    if len(msg.Content) != 2 {
        t.Fatalf("blocks = %d", len(msg.Content))
    }
    if msg.Content[0].Content[0].Text != "one" {
        t.Fatalf("first result content = %#v", msg.Content[0].Content)
    }
}
```

- [ ] Run `go test ./message` and verify it fails.

- [ ] Implement `message/message.go`:

```go
package message

import "encoding/json"

type Role string

const (
    RoleSystem    Role = "system"
    RoleUser      Role = "user"
    RoleAssistant Role = "assistant"
    RoleTool      Role = "tool"
)

type BlockType string

const (
    TypeText       BlockType = "text"
    TypeToolUse    BlockType = "tool_use"
    TypeToolResult BlockType = "tool_result"
    TypeThinking   BlockType = "thinking"
)

type Message struct {
    Role    Role    `json:"role"`
    Content []Block `json:"content,omitempty"`
}

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

type ToolUse struct {
    ID    string
    Name  string
    Input json.RawMessage
}

func SystemText(text string) Message { return Message{Role: RoleSystem, Content: []Block{NewText(text)}} }
func UserText(text string) Message { return Message{Role: RoleUser, Content: []Block{NewText(text)}} }
func Assistant(blocks ...Block) Message { return Message{Role: RoleAssistant, Content: blocks} }
func ToolResults(results ...Block) Message { return Message{Role: RoleTool, Content: results} }

func NewText(text string) Block { return Block{Type: TypeText, Text: text} }
func NewThinking(text string) Block { return Block{Type: TypeThinking, Text: text} }
func NewToolUse(id, name string, input json.RawMessage) Block { return Block{Type: TypeToolUse, ID: id, Name: name, Input: input} }
func NewToolResult(toolUseID, name string, content []Block, isError bool) Block {
    return Block{Type: TypeToolResult, ToolUseID: toolUseID, Name: name, Content: content, IsError: isError}
}

func (m Message) Text() string {
    out := ""
    for _, block := range m.Content {
        if block.Type == TypeText {
            out += block.Text
        }
    }
    return out
}

func (m Message) ToolUses() []ToolUse {
    out := []ToolUse{}
    for _, block := range m.Content {
        if block.Type == TypeToolUse {
            out = append(out, ToolUse{ID: block.ID, Name: block.Name, Input: block.Input})
        }
    }
    return out
}

func HasSystem(messages []Message) bool {
    for _, msg := range messages {
        if msg.Role == RoleSystem {
            return true
        }
    }
    return false
}
```

- [ ] Run `go test ./message`.

Expected: PASS.

---

## Task 3: Tool Package

**Files:**
- Create: `tool/tool.go`
- Create: `tool/tool_test.go`

### Requirements

- `Result.Content` is `[]message.Block`.
- `NewFunc` supports functions returning either `string` or `tool.Result` through one generic constructor.
- JSON schema inference is intentionally limited; users can pass `WithSchema` for complex schemas.

- [ ] Write `tool/tool_test.go`:

```go
package tool

import (
    "context"
    "encoding/json"
    "strings"
    "testing"

    "github.com/cloudwego/fino/message"
)

type searchInput struct {
    Query string `json:"query" jsonschema:"description=Search query"`
}

func TestNewFuncStringResult(t *testing.T) {
    search, err := NewFunc("search", "Search docs", func(ctx context.Context, in searchInput) (string, error) {
        return "found: " + in.Query, nil
    })
    if err != nil {
        t.Fatalf("NewFunc error: %v", err)
    }
    result, err := search.Run(context.Background(), json.RawMessage(`{"query":"go"}`))
    if err != nil {
        t.Fatalf("Run error: %v", err)
    }
    if got := result.Text(); got != "found: go" {
        t.Fatalf("Text() = %q", got)
    }
}

func TestNewFuncResultReturn(t *testing.T) {
    // This intentionally omits explicit type arguments. It verifies that Go can
    // infer R=Result for the single NewFunc constructor.
    custom, err := NewFunc("custom", "Custom result", func(ctx context.Context, in searchInput) (Result, error) {
        return Result{Content: []message.Block{message.NewText("block")}}, nil
    })
    if err != nil {
        t.Fatalf("NewFunc error: %v", err)
    }
    result, err := custom.Run(context.Background(), json.RawMessage(`{"query":"go"}`))
    if err != nil {
        t.Fatalf("Run error: %v", err)
    }
    if got := result.Text(); got != "block" {
        t.Fatalf("Text() = %q", got)
    }
}

func TestNewFuncRejectsMissingName(t *testing.T) {
    _, err := NewFunc("", "Search docs", func(ctx context.Context, in searchInput) (string, error) { return "", nil })
    if err == nil {
        t.Fatal("expected error")
    }
}

func TestSchemaIncludesField(t *testing.T) {
    search, err := NewFunc("search", "Search docs", func(ctx context.Context, in searchInput) (string, error) { return "", nil })
    if err != nil {
        t.Fatalf("NewFunc error: %v", err)
    }
    schema := string(search.Info().InputSchema)
    if !strings.Contains(schema, `"query"`) {
        t.Fatalf("schema missing query: %s", schema)
    }
}
```

- [ ] Implement `tool/tool.go` with these exact public shapes:

```go
type Info struct {
    Name        string
    Description string
    InputSchema json.RawMessage
    Metadata    map[string]any
}

type Result struct {
    Content []message.Block
    IsError bool
}

type Tool interface {
    Info() Info
    Run(context.Context, json.RawMessage) (Result, error)
}

type FuncReturn interface {
    ~string | Result
}

func NewFunc[T any, R FuncReturn](name, description string, fn func(context.Context, T) (R, error), opts ...Option) (Tool, error)
```

Implementation details:

- Empty `name` returns an error.
- Nil function returns an error.
- `string` return is wrapped as `Result{Content: []message.Block{message.NewText(s)}}`.
- `Result` return is passed through.
- `WithSchema(json.RawMessage)` overrides inferred schema.
- `WithMetadata(key string, value any)` records metadata.
- `GenerateSchema[T]` supports exported struct fields, `json` tag names, `omitempty`, basic scalar types, arrays, maps, and struct as object. Complex constraints are outside v1.

- [ ] Run `go test ./tool`.

Expected: PASS.

---

## Task 4: Model Package

**Files:**
- Create: `model/model.go`
- Create: `model/model_test.go`

- [ ] Write `model/model_test.go`:

```go
package model

import (
    "context"
    "iter"
    "testing"

    "github.com/cloudwego/fino/message"
    "github.com/cloudwego/fino/tool"
)

type fakeModel struct{}

func (fakeModel) Generate(ctx context.Context, messages []message.Message, tools []tool.Info, opts ...Option) (*message.Message, error) {
    msg := message.Assistant(message.NewText("ok"))
    return &msg, nil
}

func (fakeModel) Stream(ctx context.Context, messages []message.Message, tools []tool.Info, opts ...Option) iter.Seq2[Event, error] {
    return func(yield func(Event, error) bool) {
        yield(ContentBlockStart{Index: 0, Block: message.NewText("")}, nil)
        yield(TextDelta{Text: "ok"}, nil)
        yield(ContentBlockStop{Index: 0, Block: message.NewText("ok")}, nil)
        yield(FinalMessage{Message: message.Assistant(message.NewText("ok"))}, nil)
    }
}

func TestModelInterface(t *testing.T) {
    var _ Model = fakeModel{}
}

func TestOptions(t *testing.T) {
    cfg := newConfig([]Option{WithTemperature(0.7), WithMaxTokens(10)})
    if cfg.temperature == nil || *cfg.temperature != 0.7 {
        t.Fatal("temperature not applied")
    }
    if cfg.maxTokens == nil || *cfg.maxTokens != 10 {
        t.Fatal("max tokens not applied")
    }
}
```

- [ ] Implement `model/model.go` with:

```go
type Model interface {
    Generate(ctx context.Context, messages []message.Message, tools []tool.Info, opts ...Option) (*message.Message, error)
    Stream(ctx context.Context, messages []message.Message, tools []tool.Info, opts ...Option) iter.Seq2[Event, error]
}
```

Event types:

```go
type Event interface { event() }

func (ContentBlockStart) event() {}
func (ContentBlockDelta) event() {}
func (ContentBlockStop) event() {}
func (TextDelta) event() {}
func (ToolCall) event() {}
func (ToolResult) event() {}
func (Handoff) event() {}
func (FinalMessage) event() {}
func (Error) event() {}
```

- `ContentBlockStart{Index int, Block message.Block}`
- `ContentBlockDelta{Index int, Block message.Block}`
- `ContentBlockStop{Index int, Block message.Block}`
- `TextDelta{Text string}`
- `ToolCall{Call message.ToolUse}`
- `ToolResult{CallID string, Name string, Result tool.Result}`
- `Handoff{Target string}`
- `FinalMessage{Message message.Message}`
- `Error{Err error}`

Options:

- `WithTemperature(float32)`
- `WithMaxTokens(int)`

- [ ] Run `go test ./model`.

Expected: PASS.

---

## Task 5: Agent Package

**Files:**
- Create: `agent/agent.go`
- Create: `agent/agent_test.go`

- [ ] Write `agent/agent_test.go`:

```go
package agent

import (
    "context"
    "encoding/json"
    "testing"

    "github.com/cloudwego/fino/model"
    "github.com/cloudwego/fino/tool"
)

type namedTool struct{ name string }

func (t namedTool) Info() tool.Info { return tool.Info{Name: t.name, Description: t.name} }
func (t namedTool) Run(context.Context, json.RawMessage) (tool.Result, error) { return tool.Result{}, nil }

func TestNewModeWithToolsAndModelOptions(t *testing.T) {
    mode, err := NewMode("code", "write code", WithTools(namedTool{"read"}), WithModelOptions(model.WithTemperature(0)))
    if err != nil {
        t.Fatalf("NewMode error: %v", err)
    }
    if mode.Name != "code" || len(mode.Tools) != 1 || len(mode.ModelOptions) != 1 {
        t.Fatalf("mode = %#v", mode)
    }
}

func TestNewModeRejectsDuplicateToolNames(t *testing.T) {
    _, err := NewMode("code", "write code", WithTools(namedTool{"read"}, namedTool{"read"}))
    if err == nil {
        t.Fatal("expected duplicate tool error")
    }
}

func TestNewRejectsDuplicateModes(t *testing.T) {
    first, err := NewMode("code", "first")
    if err != nil {
        t.Fatalf("NewMode first error: %v", err)
    }
    second, err := NewMode("code", "second")
    if err != nil {
        t.Fatalf("NewMode second error: %v", err)
    }
    _, err = New("coder", WithMode(first), WithMode(second), WithDefaultMode("code"))
    if err == nil {
        t.Fatal("expected duplicate mode error")
    }
}

func TestNewRejectsMissingDefaultMode(t *testing.T) {
    mode, err := NewMode("code", "write code")
    if err != nil {
        t.Fatalf("NewMode error: %v", err)
    }
    _, err = New("coder", WithMode(mode), WithDefaultMode("plan"))
    if err == nil {
        t.Fatal("expected missing default mode error")
    }
}

func TestNewHandoffToolExposesTargetAgent(t *testing.T) {
    mode, err := NewMode("code", "write code")
    if err != nil {
        t.Fatalf("NewMode error: %v", err)
    }
    target, err := New("target", WithMode(mode), WithDefaultMode("code"))
    if err != nil {
        t.Fatalf("New target error: %v", err)
    }
    h, err := NewHandoffTool(target)
    if err != nil {
        t.Fatalf("NewHandoffTool error: %v", err)
    }
    handoff, ok := h.(HandoffTool)
    if !ok {
        t.Fatalf("handoff tool does not implement HandoffTool")
    }
    if handoff.TargetAgent() != target {
        t.Fatal("wrong target agent")
    }
}
```

- [ ] Run `go test ./agent` and verify it fails.

- [ ] Implement `agent/agent.go` with:

```go
type Mode struct {
    Name         string
    Instructions string
    Tools        []tool.Tool
    ModelOptions []model.Option
    Metadata     map[string]any
}

func NewMode(name, instructions string, opts ...ModeOption) (Mode, error)
func WithTools(tools ...tool.Tool) ModeOption
func WithModelOptions(opts ...model.Option) ModeOption
func WithMetadata(key string, value any) ModeOption

type Agent struct {
    name        string
    defaultMode string
    modes       map[string]Mode
}
func New(name string, opts ...Option) (*Agent, error)
func WithMode(mode Mode) Option
func WithDefaultMode(name string) Option
func (a *Agent) Name() string
func (a *Agent) DefaultMode() string
func (a *Agent) Mode(name string) (Mode, bool)

type HandoffTool interface {
    tool.Tool
    TargetAgent() *Agent
}

func NewHandoffTool(target *Agent) (tool.Tool, error)
```

Rules:

- `NewMode` checks duplicate tool names and nil tools.
- `New` checks duplicate mode names.
- Handoff tool name is `handoff_to_<agent name>`.
- Handoff tool result is text naming target, but Runner switches by `TargetAgent()` type assertion.

- [ ] Run `go test ./agent`.

Expected: PASS.

---

## Task 6: Policy and Hooks Packages

**Files:**
- Create: `policy/policy.go`
- Create: `policy/policy_test.go`
- Create: `hooks/hooks.go`

- [ ] Implement `policy.Policy`:

```go
type Policy interface {
    Authorize(context.Context, Request) (Decision, error)
}

type Request struct {
    AgentName string
    ModeName  string
    Tool      tool.Info
    Input     json.RawMessage
}

type Decision struct {
    Allow  bool
    Reason string
}

type AllowAll struct{}
```

- [ ] Add `policy/policy_test.go` verifying `AllowAll` allows.

- [ ] Implement `hooks.Hooks` with `BeforeModel`, `AfterModel`, `BeforeTool`, `AfterTool`, `OnError` and payload structs.

- [ ] Run `go test ./policy ./hooks`.

Expected: PASS.

---

## Task 7: Runner Run Loop

**Files:**
- Create: `runner/runner.go`
- Create: `runner/runner_test.go`

### Tests to write first

Write `runner/runner_test.go` with fake models and tools that define these tests:

- Final answer without tools.
- Single tool call executes and next model response returns final text.
- Multiple tool calls append one `RoleTool` message with multiple `tool_result` blocks.
- `runner.Messages(history)` containing `RoleSystem` returns `ErrSystemMessageInHistory`.
- Mode model options and run model options are both passed to model; run options are appended after mode options.
- Default max turns is 10 model calls.
- `WithMaxTurns(20)` overrides the default.
- Missing tool returns error wrapping `ErrToolNotFound`.
- Max turns returns error wrapping `ErrMaxTurns`.
- Policy denial returns `ToolDeniedError` and does not call tool.
- Policy failure returns policy error directly.
- Handoff tool switches current Agent and continues with target Agent default mode.
- Handoff result reports `LastAgent` as the target Agent and `LastMode` as the target Agent default mode.
- Context cancellation before a tool call returns `context.Canceled`.

Use a shared fake model that implements both model methods, even before stream-specific tests exist:

```go
type scriptedModel struct {
    responses []message.Message
    calls     [][]message.Message
    opts      [][]model.Option
}

func (m *scriptedModel) Generate(ctx context.Context, messages []message.Message, tools []tool.Info, opts ...model.Option) (*message.Message, error) {
    m.calls = append(m.calls, append([]message.Message(nil), messages...))
    m.opts = append(m.opts, append([]model.Option(nil), opts...))
    if len(m.responses) == 0 {
        return nil, errors.New("no scripted response")
    }
    msg := m.responses[0]
    m.responses = m.responses[1:]
    return &msg, nil
}

func (m *scriptedModel) Stream(ctx context.Context, messages []message.Message, tools []tool.Info, opts ...model.Option) iter.Seq2[model.Event, error] {
    return func(yield func(model.Event, error) bool) {
        msg, err := m.Generate(ctx, messages, tools, opts...)
        if err != nil {
            yield(model.StreamError{Err: err}, err)
            return
        }
        yield(model.FinalMessage{Message: *msg}, nil)
    }
}
```

### Implementation requirements

Public API:

```go
func New(m model.Model, opts ...Option) (*Runner, error)
func WithMaxTurns(n int) Option
func WithPolicy(p policy.Policy) Option
func WithHooks(h *hooks.Hooks) Option

type Input struct {
    messages []message.Message
}
func Text(text string) Input
func Messages(messages []message.Message) Input

type Result struct {
    Message   message.Message
    Messages  []message.Message
    LastAgent *agent.Agent
    LastMode  string
}

func (r *Result) Text() string

func WithMode(name string) RunOption
func WithModelOptions(opts ...model.Option) RunOption

func (r *Runner) Run(ctx context.Context, a *agent.Agent, input Input, opts ...RunOption) (*Result, error)
```

Typed errors:

```go
var ErrMaxTurns = errors.New("max turns exceeded")
var ErrToolNotFound = errors.New("tool not found")
var ErrSystemMessageInHistory = errors.New("system message in history")
var ErrToolDenied = errors.New("tool denied")

type ToolDeniedError struct {
    Tool     tool.Info
    Decision policy.Decision
}
```

Loop rules:

- Default `MaxTurns` is 10.
- A turn is one model call. Multiple tool calls in one model response still count as one turn. After handoff, the target Agent's next model call is a new turn.
- Keep run history without Runner-injected system messages.
- For each model call, build messages as `[system(currentMode.Instructions)] + run history`.
- Reject system role in explicit input.
- Run tools serially in model order.
- Build one tool message per assistant turn using `message.ToolResults(...)`.
- For handoff tool, execute tool result, append it, switch current agent to target, set current mode to the target default mode, and continue next turn.
- Set `Result.LastAgent` and `Result.LastMode` from the final current Agent and Mode.
- Each turn checks `ctx.Err()` before model call and before each tool call.
- Hooks fire in deterministic order: BeforeModel, AfterModel, BeforeTool, AfterTool, OnError.

- [ ] Run `go test ./runner`.

Expected: PASS.

---

## Task 8: Runner Stream Loop

**Files:**
- Modify: `runner/runner.go`
- Modify: `runner/runner_test.go`

### Tests to write first

Add stream tests with these exact behaviors:

- Stream forwards `ContentBlockStart`, `TextDelta`, `ContentBlockStop`, and `FinalMessage` from model.
- Stream waits for `FinalMessage` before parsing tool calls.
- Stream emits `model.ToolCall` before each tool run.
- Stream emits `model.ToolResult` after each tool run.
- Stream emits `model.Handoff` when switching target Agent.
- Stream applies Policy and Hooks with the same semantics as `Run`.
- Stream calls `BeforeModel` before each `model.Stream` call and calls `AfterModel` only after receiving that turn's `model.FinalMessage`.
- Stream calls `OnError` exactly once for each terminal stream error before yielding `model.StreamError{Err: err}, err`.
- Stream returns typed errors as terminal iterator errors when policy denies, tool missing, context cancels, or max turns is exceeded.

### Implementation requirements

Add:

```go
func (r *Runner) Stream(ctx context.Context, a *agent.Agent, input Input, opts ...RunOption) iter.Seq2[model.Event, error]
```

Rules:

- Share setup logic with `Run` through unexported helpers.
- Yield model events immediately.
- Fire `BeforeModel` before calling `model.Stream`.
- Capture current turn final message from `model.FinalMessage`.
- Fire `AfterModel` after receiving `model.FinalMessage`.
- If no final message arrives, yield `model.StreamError{Err: err}, err` and stop.
- After final message, execute tools using the same serial tool path as `Run`.
- Yield `model.ToolCall`, `model.ToolResult`, and `model.Handoff` from Runner-generated events.
- For every terminal error, yield exactly one final pair `model.StreamError{Err: err}, err` and stop iteration. Consumers can use `errors.Is` / `errors.As` on the second value.

- [ ] Run `go test ./runner`.

Expected: PASS.

---

## Task 9: Public Example and README

**Files:**
- Create: `agent/example_test.go`
- Modify: `README.md`

- [ ] Add executable example showing minimal use: `NewMode`, `New`, `runner.New`, `runner.Run`.

- [ ] Update README with the same minimal shape and boundary statement:

```text
Core APIs use required arguments plus options. The SDK owns the ReAct loop and interfaces; users own orchestration, persistence, RAG, MCP, provider clients, and permission semantics.
```

- [ ] Run `go test ./...`.

Expected: PASS.

---

## Task 10: Final Verification

- [ ] Run `gofmt -w .`.
- [ ] Run `go test ./...`.
- [ ] Verify these paths do not exist:

```text
graph/
rag/
session/
mcp/
tools/filesystem/
tools/bash/
providers/anthropic/
providers/openai/
providers/gemini/
```

- [ ] Run `git diff --stat` and confirm only planned core files changed.

---

## Self-Review Checklist

- Runner.Stream is in plan and tests.
- Handoff switching is in plan and tests.
- Model options from Mode and RunOption are in plan and tests.
- Tool result batching is in plan and tests.
- System message rejection is in plan and tests.
- Policy denial vs failure is in plan and tests.
- Duplicate tool and duplicate mode validation are in plan and tests.
- Stream events include content block start/delta/stop and handoff.
- Tool result uses `[]message.Block`.
- Typed errors are specified.
- No graph, RAG, MCP, session store, built-in tools, or provider clients are in scope.
