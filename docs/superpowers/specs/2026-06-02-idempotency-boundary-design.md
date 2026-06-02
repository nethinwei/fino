# PR6: Idempotency Boundary — Design Spec

## Problem

Recovery, retry, audit, and external deduplication all need a stable identifier
for "which run" and "which tool call within that run."  Today the Runner provides
no such identifiers: tool implementations receive only the caller's `context.Context`
and their JSON input.  An `x/recover` snapshot or a `ResumeApproved` resumption
cannot carry a run-scoped key, so add-ons must invent their own — and those
invented keys cannot be correlated with the Runner's own execution trace.

The roadmap (PR6) calls for defining idempotency keys and retry constraints
_without_ adding automatic tool retries.  This spec delivers the minimal
contract: a stable execution context that the Runner injects and tools (and
`x/` add-ons) can read.

## Scope

**In scope:**

- Define `tool.ExecutionContext` and `tool.ExecutionContextFrom(ctx)`.
- Add `runner.WithRunID(id)` as a per-run option.
- Runner injects `ExecutionContext` into the `context.Context` passed to every
  tool's `Run` method.
- Extend `SuspendedRun` with `RunID` so `ResumeApproved` recovers the key.
- Update loop semantics with invariant I13.
- Extend `x/replay.ToolRecord` with `CallID`.

**Out of scope:**

- Automatic tool retries (including for `Effects.ReadOnly` or
  `Effects.Idempotent`).
- Refusing execution when `RunID` is empty.
- `InputHash`, cryptographic signatures, or tamper detection.
- Session, checkpoint, or store abstractions.
- Batch partitioning based on `Effects.Idempotent` or `Effects.Destructive`.

## API

### `tool.ExecutionContext`

```go
package tool

type ExecutionContext struct {
	RunID          string
	ToolCallID     string
	IdempotencyKey string
}

func ExecutionContextFrom(ctx context.Context) (ExecutionContext, bool)
```

- `RunID`: caller-supplied via `runner.WithRunID`.  Empty when not provided.
- `ToolCallID`: the `message.ToolUse.ID` from the model's tool_use block
  (the same value surfaced as `Approval.CallID` and `ToolRecord.CallID`).
  **Precondition:** the model provider must supply unique, non-empty IDs for
  every `tool_use` block within a run.  Real providers (Anthropic `toolu_*`,
  OpenAI `call_*`) satisfy this in practice.  If a provider reuses an ID
  across turns or emits an empty ID, the `IdempotencyKey` degrades to
  non-unique and the idempotency guarantee silently breaks — the Runner does
  not validate this at runtime (consistent with the "no refusal on empty
  RunID" out-of-scope).
- `IdempotencyKey`: when `RunID != ""`, derived as `RunID + ":" + ToolCallID`;
  when `RunID == ""`, the empty string.  The derivation rule is an
  implementation detail, not a public API — tools read `IdempotencyKey`, they
  do not construct it.

`ExecutionContextFrom` returns `(ExecutionContext{}, false)` when the context
carries no execution context (e.g. in replay, unit tests, or tool
implementations called outside the Runner).

The `tool` package exports both the reader and a setter:

```go
func ContextWithExecutionContext(ctx context.Context, ec ExecutionContext) context.Context
```

The `runner` package imports `tool` and calls `ContextWithExecutionContext` to
inject the context before each tool call.  `tool` does not import `runner`.
The setter is exported (Go requires this for cross-package access) but its
primary caller is the Runner; tool authors call `ExecutionContextFrom`.
Because the setter is exported, any package can inject a fabricated
`ExecutionContext`.  The execution context is **not a security boundary**:
tools must not make authorization decisions based on its contents.  It is an
audit and deduplication hint, not a trusted credential.

### `runner.WithRunID`

```go
package runner

func WithRunID(id string) RunOption
```

Stores the run ID in `runConfig`.  Shared by `Run`, `Stream`, and
`ResumeApproved`.  When `WithRunID` is not provided, `RunID` is the empty
string and `IdempotencyKey` is also empty — fully backward-compatible.

### `SuspendedRun` extension

```go
type SuspendedRun struct {
	Messages      []message.Message
	LastAgentName string
	LastMode      string
	PendingCalls  []PendingToolCall
	RunID         string
}
```

`Result.SuspendedRun()` populates `RunID` from `runConfig.RunID`.
`ResumeApproved` uses `suspended.RunID` as the run ID for the resumed batch,
so approved/allowed calls receive the same `IdempotencyKey` they would have
received in the original (non-suspended) execution.

### `x/replay.ToolRecord` extension

```go
type ToolRecord struct {
	Name   string          `json:"name"`
	CallID string          `json:"callID,omitempty"`
	Input  json.RawMessage `json:"input,omitempty"`
	Result tool.Result     `json:"result"`
	Err    string          `json:"err,omitempty"`
}
```

`RecordingTool` populates `CallID` from the execution context when present.
Because `ToolRecord` is embedded in the PR4 `ToolExecutionEvent`, adding
`CallID` automatically gives the execution tape per-call correlation — audit
and idempotency share the same identifier.
`ReplayTool` does **not** inject `ExecutionContext` — replay tools perform no
real side effects and do not need idempotency keys.

## Data Flow

```
caller ── runner.WithRunID("run_123") ──► runConfig.RunID
  │
  ▼
Runner.execute(ctx, st, tool, call)
  │  constructs ExecutionContext{RunID, ToolCallID=call.ID, IdempotencyKey}
  │  injects via tool.ContextWithExecutionContext(ctx, ec)
  ▼
tool.Run(ctx, input)
  │  tool reads: ec, ok := tool.ExecutionContextFrom(ctx)
  ▼
tool uses ec.IdempotencyKey for external dedup / audit
```

Resume path:

```
ResumeApproved(ctx, a, suspended, approvals)
  │  uses suspended.RunID as runConfig.RunID
  ▼
resumeExecuteBatch → r.execute(ctx, st, tool, call)
  │  same injection path; approved calls get same IdempotencyKey
```

## Loop Semantics Update

### §4.1 / §4.2 addition

Before each tool's `Run`, the Runner constructs `ExecutionContext` from
`runConfig.RunID` and `call.ID` and injects it into the context **before**
calling `beforeTool`, so lifecycle hooks (and the tool itself) can read the
execution context via `tool.ExecutionContextFrom(ctx)`.  The `IdempotencyKey`
is a stable function of `(RunID, ToolCallID)`: the same `ToolCallID` within
the same run always produces the same key, regardless of serial vs. parallel
path.

### New invariant I13

> **I13 (idempotency-key stability):** Given a non-empty `RunID`, the
> `IdempotencyKey` injected for any `ToolCallID` is a deterministic function
> of `(RunID, ToolCallID)`, independent of the serial, parallel, or
> ResumeApproved execution path.

### §7.3 (approval resume)

`ResumeApproved` restores `suspended.RunID` into `runConfig`.  Approved and
previously-allowed calls therefore receive the same `ExecutionContext` they
would have received had the batch not suspended.

## Compatibility

- Fully backward compatible.  `WithRunID` is a new option; omitting it
  preserves PR5 behavior exactly.
- `SuspendedRun.RunID` is a new field; JSON fixtures without it deserialize
  to the zero value (`""`), which is the correct default.
- `ToolRecord.CallID` is a new field; JSON fixtures without it deserialize to
  `""`, which is the correct default.
- `tool.ExecutionContext` is additive; existing tools that do not call
  `ExecutionContextFrom` are unaffected.
- The `Tool.Run` signature is unchanged.

## Not Done

- No automatic retries for any tool, including `Effects.ReadOnly` or
  `Effects.Idempotent` tools.
- No refusal to execute when `RunID` is empty.
- No `InputHash`, signature, or tamper detection on `SuspendedRun`.
- No session, checkpoint, or store in core.
- No batch partitioning based on `Effects.Idempotent` or `Effects.Destructive`.
