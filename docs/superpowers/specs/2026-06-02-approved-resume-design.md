# PR3: Approved Resume Design

## Summary

Add a first-class `ResumeApproved` API that executes the original pending tool calls after human approval, rather than asking the model to regenerate them. A suspended run produces a `SuspendedRun` snapshot; the caller collects approvals and passes them to `ResumeApproved`, which validates, executes approved calls, writes rejections as visible `tool_result` blocks, and continues the ReAct loop.

## Motivation

PR2 added `DecisionSuspend` and `Result{Suspended: true, PendingCalls: [...]}`, but the only resume mechanism is `WithResumeFromPendingTools()` — a blind seam that re-executes all pending tool calls without approval binding. It cannot:

- Distinguish approved calls from rejected ones.
- Write rejection reasons as model-visible `tool_result` blocks.
- Validate that approvals correspond to the suspended calls.

Without `ResumeApproved`, HITL approval requires the caller to manually reconstruct tool results for rejected calls, or use the blind seam and hope no rejected call slips through.

## Design Decisions

### D1: SuspendedRun is a value type, not a new state

```go
type SuspendedRun struct {
    Messages     []message.Message
    LastMode     string
    PendingCalls []PendingToolCall
}
```

`SuspendedRun` is a plain data snapshot extracted from a suspended `Result`. It is not a checkpoint, not a state machine, and not persisted by the core. The caller serializes it however they want (JSON, database, file).

`PendingToolCall` gains `AgentName` and `ModeName` fields (present in `policy.Request` but absent from the current PR2 struct), so `ResumeApproved` can re-authorize calls in the correct context. No `InputHash` field — tamper detection is a user-level concern, not a core seam (see Rationale below).

**Rationale for omitting InputHash**: A hash binds the approval to the observed input, but if the caller can modify `SuspendedRun.Messages`, they can also modify `InputHash`. Within a single process, hash verification provides no real security gain. Cross-process tamper detection requires cryptographic signatures, which are out of scope for the core. Users who need this can add it in `x/` or application code.

### D2: Approval binds by CallID

```go
type Approval struct {
    CallID   string
    Approved bool
    Reason   string
}
```

Each approval matches a pending call by `CallID` (the `tool_use` block's ID). No hash binding. The `Reason` field is written into the `tool_result` for rejected calls so the model can observe why a call was refused.

### D3: ResumeApproved validates, then executes, then continues

```go
func (r *Runner) ResumeApproved(
    ctx context.Context,
    a *agent.Agent,
    suspended SuspendedRun,
    approvals []Approval,
    opts ...RunOption,
) (*Result, error)
```

Flow:

1. **Validate** `SuspendedRun`: last message must be a dangling assistant with tool_use blocks; every `PendingCall.Call.ID` must appear in those blocks; no duplicate pending calls.
2. **Validate approvals**: every pending call must have exactly one approval; no approval may reference an unknown CallID; no duplicate approvals.
3. **Execute batch**: for each tool_use in call order:
   - If the call is in `PendingCalls` (was suspended): look up its approval. Approved → execute the tool. Rejected → write `tool_result{IsError: true, Content: [text("rejected: <reason>")]}`.
   - If the call is NOT in `PendingCalls` (was allowed but not executed due to batch suspend): execute it. No approval needed — it was already authorized.
4. **Append single RoleTool message** with all results in call order.
5. **Continue ReAct loop** from the next model turn.

This means `ResumeApproved` is a loop entry point that starts at step 3 of `[T-TOOLS]` (executeBatch), not at `[T-MODEL]`. The model is not called until after the batch completes.

### D4: Rejections are model-visible, not hidden control flow

When a call is rejected (`Approved: false`), the Runner does NOT execute the tool. Instead it appends a `tool_result` block with `IsError: true` and content describing the rejection reason. This makes the rejection observable by the model, which can then adjust its behavior.

The model sees:

```text
assistant: [tool_use: call_1, "delete_file", {path: "/tmp/x"}]
tool:      [tool_result: call_1, IsError: true, "rejected: user denied deletion"]
```

### D5: ResumeApproved does NOT re-authorize

The suspended calls were already authorized (they passed through `authorizeBatch` and received `DecisionSuspend` or `DecisionAllow`). `ResumeApproved` does not call `Policy.Authorize` again. Approval by a human replaces policy authorization for the suspended calls.

For previously-allowed calls in the batch, they were already authorized and approved-by-definition; they execute directly.

**Rationale**: Re-authorization would require the original Policy to be present and could produce different decisions (e.g., a time-based policy that now denies). The whole point of suspend + approve is that human approval is the final decision.

### D6: Result.SuspendedRun() helper

```go
func (r *Result) SuspendedRun() (SuspendedRun, error)
```

Returns an error if `Suspended` is false. This is a convenience method; callers could build `SuspendedRun` manually from `Result` fields, but the helper ensures consistency (particularly the `AgentName`/`ModeName` fields on `PendingToolCall`).

### D7: Relationship to WithResumeFromPendingTools

`WithResumeFromPendingTools` is the lower-level seam from PR2. `ResumeApproved` is the higher-level API for the approval use case. They coexist:

- `WithResumeFromPendingTools`: blind resume, executes all pending tools, no approval. Useful for crash recovery and `x/recover`.
- `ResumeApproved`: approval-gated resume, executes only approved calls, writes rejections as results. Useful for HITL.

`ResumeApproved` does NOT use `WithResumeFromPendingTools` internally. It has its own entry point that validates the `SuspendedRun` and processes approvals before executing.

### D8: Stream not extended for PR3

PR2 downgrades `DecisionSuspend` to `ToolDeniedError` in `Stream`. PR3 does not change this. `ResumeApproved` is a `Run`-only API. Extending `Stream` with suspend/resume events is deferred — it requires new event types and would expand the stream contract for all consumers.

### D9: PendingToolCall extended fields

```go
type PendingToolCall struct {
    Tool      tool.Info
    Call      message.ToolUse
    Reason    string
    AgentName string // new: agent that was active when the call was suspended
    ModeName  string // new: mode that was active when the call was suspended
}
```

These fields are needed so `ResumeApproved` can identify which agent/mode context the call belongs to, without requiring the caller to pass this information separately.

## Changes by Package

### runner/runner.go

| Change | Detail |
|--------|--------|
| Extend `PendingToolCall` | Add `AgentName`, `ModeName` fields |
| Add `SuspendedRun` struct | `Messages`, `LastMode`, `PendingCalls` |
| Add `Approval` struct | `CallID`, `Approved`, `Reason` |
| Add `Result.SuspendedRun()` | Helper to extract snapshot from suspended result |
| Add `Runner.ResumeApproved()` | Validate → execute approved batch → continue loop |
| Update `authorizeBatch` | Populate `AgentName`/`ModeName` on `PendingToolCall` |

### runner/runner_stream.go

No changes. Stream suspend remains downgraded to deny.

### docs/spec/loop-semantics.md

| Change | Detail |
|--------|--------|
| §3 new transfer rule | `[T-RESUME]` — approved resume entry point |
| §7.2 split | Separate safe-boundary recovery (I10) from approval resume |
| §7 new invariant I12 | After `ResumeApproved`, every `tool_use` in the suspended batch has a corresponding `tool_result` in the appended `RoleTool` message |

### docs/design.md

| Change | Detail |
|--------|--------|
| 人工确认和恢复 section | Update: `ResumeApproved` is now a first-class API, not just "re-call Run with history" |

## Updated Transfer Rules

```text
[T-RESUME]   pre: ResumeApproved called with valid SuspendedRun + approvals
              step 1 validate:
                SuspendedRun.Messages tail = dangling assistant with tool_use blocks
                PendingCalls ⊆ tool_use blocks (by Call.ID)
                approvals: one per PendingCall, no unknown/duplicate CallIDs
                validation failure ⟶ return error (not OnError; pre-loop)
              step 2 executeBatch:
                For each tool_use cᵢ in call order:
                  if cᵢ ∈ PendingCalls (suspended):
                    approval = lookup by CallID
                    if Approved → resolve tool, execute, produce tool_result
                    if !Approved → produce tool_result{IsError: true, "rejected: <Reason>"}
                  else (was Allow, not executed due to batch suspend):
                    resolve tool, execute, produce tool_result
              step 3 append:
                history = SuspendedRun.Messages ++ [ToolResults(r₁ … rₙ)]
              step 4 continue loop from [T-MODEL]
```

## Typed Errors

```go
var ErrNotSuspended = errors.New("result is not suspended")

type ApprovalError struct {
    Missing  []string // CallIDs with no approval
    Unknown  []string // Approval CallIDs not in pending calls
    Duplicate []string // CallIDs with multiple approvals
}
```

`ApprovalError` wraps validation failures so callers can distinguish "bad input" from "tool execution failed".

## Backward Compatibility

| Scenario | Behavior |
|----------|----------|
| Existing `WithResumeFromPendingTools` usage | Unchanged. Still works for blind resume. |
| Existing `PendingToolCall{Tool, Call, Reason}` | New fields are zero-value; old code that doesn't set them still compiles. `ResumeApproved` requires them to be set (via `Result.SuspendedRun()`). |
| Existing `Result.Suspended` | Unchanged. |
| Old Policy returning `DecisionSuspend` | Unchanged. |

## Out of Scope (PR4+)

- Input hash / tamper detection on `PendingToolCall` (user-level concern; `x/` add-on)
- Stream suspend/resume events (requires new `model.Event` type)
- Execution tape recording of approval events (PR4)
- Effect-aware concurrency for approved resume batches (PR5)
- Batch partitioning (allowing partial execution when some calls are suspended)
- Cryptographic signatures for cross-process tamper detection

## Test Plan

1. **Unit: SuspendedRun extraction** — `Result.SuspendedRun()` returns correct snapshot from a suspended result; returns `ErrNotSuspended` from a completed result.
2. **Unit: PendingToolCall extended fields** — `AgentName` and `ModeName` are populated during authorizeBatch.
3. **Integration: approve executes tool** — Policy suspends → caller approves → `ResumeApproved` executes the tool, appends RoleTool, continues to next model turn.
4. **Integration: reject writes error result** — Policy suspends → caller rejects → `ResumeApproved` writes `tool_result{IsError: true}` with rejection reason, does NOT execute the tool.
5. **Integration: mixed allow + suspend resume** — Batch had call₁=Allow, call₂=Suspend. ResumeApproved executes both (call₁ without approval, call₂ with approval).
6. **Integration: rejection is model-visible** — After reject, the model sees `tool_result{IsError: true, "rejected: ..."}` and can adjust its next response.
7. **Integration: no re-authorization** — `ResumeApproved` does not call `Policy.Authorize`. Verified by a Policy that would deny if called again.
8. **Integration: validation errors** — Missing approval, unknown CallID, duplicate approval, non-suspended result all return `ApprovalError` without executing any tool.
9. **Integration: handoff in approved batch** — Handoff tool in resumed batch applies batch-terminal, last-wins.
10. **Integration: OnError not triggered on validation failure** — Pre-loop validation errors are returned directly.
11. **Integration: OnError triggered on execution error** — Tool execution error during resume triggers OnError once.
12. **Integration: parallel path not affected** — `WithMaxConcurrency` does not change `ResumeApproved` behavior; the resume batch always executes serially in call order.
13. **Property: I12** — After `ResumeApproved`, every `tool_use` in the suspended batch has a corresponding `tool_result` in the appended `RoleTool` message.
