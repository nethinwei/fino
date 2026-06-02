# Execution Tape Design (PR4)

## Goal

Upgrade `x/replay` from a model/tool-only replay helper into an execution tape reference composition. The tape records the public execution decisions that make a run reproducible and auditable: model responses, policy decisions, tool executions, suspensions, approvals, resumes, and termination.

PR4 must not change the core runtime boundary. `runner`, `policy`, `hooks`, `agent`, `model`, `message`, and `tool` stay focused on the minimal ReAct kernel. Tape recording lives in `x/replay` and observes only public seams.

## Current State

Today `x/replay` records only two nondeterministic seams:

```go
type Log struct {
	mu    sync.Mutex
	Model []message.Message `json:"model"`
	Tools []ToolRecord      `json:"tools"`
}
```

`RecordingModel` records model responses. `RecordingTool` records tool executions. `ReplayModel` and `ReplayTool` use those records to avoid calling real providers or real tools.

This is enough to reproduce simple completed runs, but it does not record policy decisions, suspend boundaries, human approvals, resume attempts, or terminal status. `x/eval` therefore depends on a partial trace and cannot yet assert the full execution path required by the roadmap.

## Architecture

Use option A from brainstorming: extend `x/replay` in place with a new tape event layer while keeping the existing `Log.Model` and `Log.Tools` fields as the replay execution source.

```go
type Log struct {
	mu     sync.Mutex
	Model  []message.Message `json:"model"`
	Tools  []ToolRecord      `json:"tools"`
	Events []Event           `json:"events"`
}
```

The compatibility fields stay because they already drive replay and existing callers may persist them. `Events` is the structured audit/eval tape. It does not replace the existing replay engine in PR4.

This intentionally duplicates model responses and tool executions: `Log.Model` / `Log.Tools` remain the execution source, while `Events` is the audit layer. That redundancy is a compatibility bridge. A future version may make `Events` the single replay source once fixtures and callers have migrated, but PR4 should not rewrite the replay engine and tape schema in the same step.

The package gets three categories of API:

- Existing wrappers: `RecordingModel`, `RecordingTool`, `ReplayModel`, `ReplayTool`.
- New wrappers: `RecordingPolicy`, `ReplayPolicy`.
- Explicit recording helpers for public run boundaries: `RecordSuspend`, `RecordApproval`, `RecordResume`, `RecordTermination`.

No core package imports `x/replay`. The direction remains one-way: `x/replay` imports core packages and composes around their public APIs.

## Event Model

`Event` uses a string kind plus kind-specific payload fields. This keeps JSON fixtures readable and avoids introducing a new core abstraction.

The nested payload shape is only for audit fixtures. It is not a provider wire format and does not change the existing flat `message.Block` discriminated union used for model messages.

```go
type EventKind string

const (
	EventModelResponse  EventKind = "model_response"
	EventPolicyDecision EventKind = "policy_decision"
	EventToolExecution  EventKind = "tool_execution"
	EventSuspend        EventKind = "suspend"
	EventApproval       EventKind = "approval"
	EventResume         EventKind = "resume"
	EventTermination    EventKind = "termination"
)

type Event struct {
	Kind EventKind `json:"kind"`

	ModelResponse  *ModelResponseEvent  `json:"model_response,omitempty"`
	PolicyDecision *PolicyDecisionEvent `json:"policy_decision,omitempty"`
	ToolExecution  *ToolExecutionEvent  `json:"tool_execution,omitempty"`
	Suspend        *SuspendEvent        `json:"suspend,omitempty"`
	Approval       *ApprovalEvent       `json:"approval,omitempty"`
	Resume         *ResumeEvent         `json:"resume,omitempty"`
	Termination    *TerminationEvent    `json:"termination,omitempty"`
}
```

Each event records only data that public APIs already expose.

```go
type ModelResponseEvent struct {
	Message message.Message `json:"message"`
}

type PolicyDecisionEvent struct {
	Request  policy.Request  `json:"request"`
	Decision policy.Decision `json:"decision"`
	Err      string          `json:"err,omitempty"`
}

type ToolExecutionEvent struct {
	Record ToolRecord `json:"record"`
}

type SuspendEvent struct {
	Suspended runner.SuspendedRun `json:"suspended"`
}

type ApprovalEvent struct {
	Approvals []runner.Approval `json:"approvals"`
}

type ResumeEvent struct {
	LastAgentName string            `json:"last_agent_name"`
	LastMode      string            `json:"last_mode"`
	Approvals     []runner.Approval `json:"approvals"`
	Status        string            `json:"status"`
	Err           string            `json:"err,omitempty"`
}

type TerminationEvent struct {
	Status    string `json:"status"`
	Err       string `json:"err,omitempty"`
	FinalText string `json:"final_text,omitempty"`
}
```

`Status` values are strings to keep the event JSON stable and simple:

```go
const (
	StatusCompleted = "completed"
	StatusSuspended = "suspended"
	StatusError     = "error"
)
```

## Recording Flow

Recording is explicit composition around public seams.

```go
log := &replay.Log{}

recModel := replay.RecordingModel{Next: realModel, Log: log}
recPolicy := replay.RecordingPolicy{Next: realPolicy, Log: log}
recTool := replay.RecordingTool(realTool, log)

r, _ := runner.New(recModel, runner.WithPolicy(recPolicy))
res, err := r.Run(ctx, agentWith(recTool), input)
replay.RecordTermination(log, res, err)
```

`RecordingModel` appends both `Log.Model` and `model_response` events after a successful model response. `RecordingTool` appends both `Log.Tools` and `tool_execution` events after a tool returns, including tool errors. `RecordingPolicy` appends `policy_decision` events after `Authorize` returns, including policy errors.

Run-boundary events are not observable through `model.Model`, `tool.Tool`, or `policy.Policy`, so the caller records them explicitly after public API calls:

```go
suspended, _ := res.SuspendedRun()
replay.RecordSuspend(log, suspended)

approvals := []runner.Approval{...}
replay.RecordApproval(log, approvals)

res2, err := r.ResumeApproved(ctx, liveAgent, suspended, approvals)
replay.RecordResume(log, suspended, approvals, res2, err)
replay.RecordTermination(log, res2, err)
```

This is deliberately not magic. It does not require runner hooks for suspension, does not introduce a runner observer interface, and does not hide persistence or approval UI inside `x/replay`.

PR4 should not add a `RunRecorder` wrapper around `runner.Run` or `Runner.ResumeApproved`. Such a helper could reduce missed boundary records, but it would also expand the API surface and blur the current discipline: wrappers cover model/tool/policy seams, and public run boundaries are recorded explicitly by the caller. If repeated boilerplate becomes a real problem, add a thin `x/replay` convenience helper in a later PR without changing the core.

## Replay Behavior

Replay still avoids real providers and real tools:

- `ReplayModel` returns `Log.Model` responses in order.
- `ReplayTool` returns recorded `ToolRecord` values by tool name and input.
- New `ReplayPolicy` returns recorded `policy_decision` decisions in order.

`ReplayPolicy` consumes `policy_decision` events in order, skipping interleaved `model_response`, `tool_execution`, and boundary events (those replay from `Log.Model` / `Log.Tools` or are non-executable audit data); otherwise the first `model_response` of a normal tape would make the first `Authorize` fail. On each `Authorize` it advances to the next `policy_decision` event and validates that the current `policy.Request` matches the recorded request on stable execution identity: `AgentName`, `ModeName`, `Tool.Name`, and raw `Input`. It should not deep-compare `tool.Info.Metadata`, because user metadata can contain arbitrary JSON-like values and is not part of the replay key. If no later `policy_decision` event remains, or the next `policy_decision` does not match the stable request identity, it returns a `replay:` error. That error is a fixture mismatch, not a policy deny. If the recorded policy event contains `Err`, `ReplayPolicy` returns the recorded decision plus an error with that message.

Policy replay is important because a real policy can depend on human approval state, clocks, network RBAC systems, allowlists, or risk scoring. Replaying a run must not call those systems again.

Approval and resume events are not executable by `ReplayPolicy`. They are structured audit data and eval inputs. `resume` is a boundary result event appended after `ResumeApproved` returns, not a pre-execution marker; model/tool events produced during the resumed loop naturally appear before it. A test that covers human approval can replay to a suspended result, read approvals from the fixture, call `ResumeApproved`, record or assert the resume event, and then assert final termination.

## Eval Behavior

`x/eval` continues to run deterministic cases over `ReplayModel`, but PR4 gives it access to a complete tape.

Keep `Case` unchanged for compatibility and add a runner-option entry point:

```go
func RunWithOptions(ctx context.Context, c Case, opts ...runner.Option) error
```

`Run(ctx, c)` remains as-is and delegates to `RunWithOptions(ctx, c)`. Tests that need full tape fidelity can pass `runner.WithPolicy(&replay.ReplayPolicy{Log: c.Log})` without changing `Case`.

PR4 does not need eval to automate a whole approval UI. For approval tests, a case may perform two explicit stages outside the generic `eval.Run`: replay to suspend, extract approvals from `Log.Events`, call `ResumeApproved`, then assert the terminal result and tape events.

## JSON And Compatibility

`Log.Marshal` and `Unmarshal` include the new `Events` field while preserving `model` and `tools` fields.

Existing fixtures without `events` remain valid: `Events` unmarshals as nil, and replay still uses `Model` and `Tools`.

New fixtures can be used for both old replay and tape-based assertions. Event payloads include values such as `tool.Info.Metadata map[string]any` through public structs. `x/replay` does not sanitize those values. If callers need JSON persistence, they must keep metadata JSON-marshalable.

## Error Handling

Recording wrappers never change the wrapped component's behavior. They record what happened and return the same result and error.

Replay errors use a `replay:` prefix and mean the fixture does not match the current run wiring. Examples: no more recorded model responses, no recorded tool result for name/input, no recorded policy decision, or mismatched policy request.

`RecordTermination` classifies public outcomes:

- `err != nil` -> `error`
- `res != nil && res.Suspended` -> `suspended`
- `res != nil && !res.Suspended` -> `completed`

It does not call hooks, does not wrap errors, and does not affect runner control flow.

## Tests

PR4 should add tests that pin the tape without overextending core behavior:

1. Record a completed run and assert events appear in order: `model_response`, `policy_decision`, `tool_execution`, `model_response`, `termination`.
2. Replay that run with `ReplayModel`, `ReplayTool`, and `ReplayPolicy`; assert real model, real tool, and real policy are not called.
3. Record a policy deny; replay returns the same deny path through runner without consulting the real policy.
4. Record a suspend; tape includes `policy_decision`, `suspend`, and `termination{status:"suspended"}`.
5. Record approval plus `ResumeApproved`; tape includes `approval`, `resume`, tool execution for approved calls, and final termination.
6. JSON round trip preserves `Model`, `Tools`, and `Events`.
7. Existing `x/replay` and `x/eval` tests still pass with fixtures that do not contain events.

## Documentation Updates

Update `README.md`, `README.zh-CN.md`, and `docs/design.md` to stop saying `x/replay` records only model/tool effects. New wording should be precise:

- `x/replay` records an execution tape over public seams.
- Replay does not call real providers, tools, or policies when wired with replay wrappers.
- The tape is reproducibility and audit evidence, not proof of business correctness.
- It does not provide exactly-once side effects, durable workflow, or tamper resistance.

Update `docs/roadmap.md` after implementation to mark PR4 as completed for v0.5.0.

## Non-Goals

- No core package changes.
- No new `x/tape` package in PR4.
- No runner observer interface.
- No automatic durable checkpointing, session store, graph runtime, or workflow engine.
- No cryptographic signing, `InputHash`, tamper-proofing, or exactly-once guarantee.
- No automatic retries for tools or policies.
- No claim that a replayed result proves business correctness.
- No stream suspend/resume support beyond the existing stream behavior.

## Acceptance Criteria

- Core package import graph still has no dependency on `x/` packages.
- Existing `x/replay` APIs remain source-compatible.
- A complete tape can record model responses, policy decisions, tool executions, suspensions, approvals, resumes, and termination.
- Replay of a recorded run can avoid real model, real tool, and real policy calls.
- `x/eval` can use `ReplayPolicy` for policy-sensitive fixtures via `RunWithOptions`.
- Documentation describes tape as a reference composition, not a core capability.
