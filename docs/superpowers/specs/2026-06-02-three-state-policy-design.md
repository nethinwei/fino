# PR2: Three-State Policy Design

## Summary

Upgrade `policy.Decision` from binary allow/deny to three-state allow/deny/suspend, with a safe zero-value (`DecisionUnspecified`) that preserves backward compatibility. When a tool call is suspended, the Runner halts with a `Suspended` result carrying the pending calls, enabling PR3's approval/resume flow.

## Motivation

Current `policy.Decision` is `{Allow bool, Reason string}`. This limits Policy to binary gatekeeping. Real-world agents need a third state: "I don't have enough context to decide — suspend and ask a human." Suspend is not an error; it is a legitimate termination path that signals the caller to collect approval and resume.

Without Suspend:
- HITL approval requires Policy to return `Deny` (losing semantic fidelity) or implement ad-hoc out-of-band signaling.
- `WithResumeFromPendingTools()` exists as a seam, but no Policy can produce a "suspend" decision to trigger it cleanly.
- PR3's `ResumeApproved` has nothing to resume from.

## Design Decisions

### D1: DecisionKind with DecisionUnspecified zero value

```go
type DecisionKind uint8

const (
    DecisionUnspecified DecisionKind = iota
    DecisionAllow
    DecisionDeny
    DecisionSuspend
)
```

**Rationale**: Go zero values must be safe. `DecisionUnspecified(0)` means "legacy code that didn't set Kind." The Runner interprets it by falling back to the `Allow bool` field:

- `Kind == DecisionUnspecified && Allow == true` → treat as `DecisionAllow`
- `Kind == DecisionUnspecified && Allow == false` → treat as `DecisionDeny`

This preserves full backward compatibility: any existing `Policy` returning `Decision{Allow: true}` or `Decision{Allow: false}` continues to work without modification.

New code should set `Kind` directly and leave `Allow` at its zero value (`false`). The `Allow` field is soft-deprecated: it still works via the migration rule, but new implementations should not set it.

### D2: Suspend is a Result, not an error

Suspend is a successful termination path, not a failure. It produces a `runner.Result` with:

```go
type Result struct {
    Message      message.Message
    Messages     []message.Message
    LastAgent    *agent.Agent
    LastMode     string
    Suspended    bool
    PendingCalls []PendingToolCall
}
```

**Rationale**:

1. **Semantic clarity**: I4 (OnError once) applies only to runtime *errors*. Suspend is not an error. Making it an error would force `OnError` to fire (polluting observability) or require special-case logic to skip it.
2. **Data carrier**: Suspend must expose the full history (including the dangling `tool_use` blocks), the pending calls, and the current agent/mode. `Result` already carries `Messages`, `LastAgent`, `LastMode`. An error type would need to duplicate all of this.
3. **Seam continuity**: `WithResumeFromPendingTools()` already models "history tail has dangling tool_use → resume" as a legitimate loop entry (§7.2). The suspended Result's `Messages` naturally produces this state. PR3's `ResumeApproved` consumes `Result.Messages` directly.
4. **Minimal surface**: No new error type, no new interface. Two fields on an existing struct.

### D3: PendingToolCall minimal fields (PR2 scope)

```go
type PendingToolCall struct {
    Tool   tool.Info
    Call   message.ToolUse
    Reason string
}
```

PR3 will add `InputHash string` for approval binding. PR2 does not need it — the Runner does not validate input hashes at suspend time; that is PR3's concern.

### D4: Batch suspend semantics — first implementation

When any call in a tool batch is suspended, the entire batch is suspended and no tool in that batch executes. `PendingCalls` contains **only** the calls whose `ResolvedKind() == DecisionSuspend`; calls that were allowed but not executed (due to the batch suspend) are not included.

**Mixed Allow + Suspend batches**: When call₁ = Allow and call₂ = Suspend, the batch suspends. call₁ is not executed and does not appear in `PendingCalls`. The history ends with the assistant message containing both `tool_use` blocks (allow and suspend alike), with no following `tool_result` message. PR3's `ResumeApproved` must handle this by re-executing all pending calls (both previously-allowed and previously-suspended) after approval, since none produced results. This is the "no batch partitioning" trade-off acknowledged in Out of Scope.

**Rationale**: This is the conservative first version. It avoids partial execution and the question of "which tools ran before the suspend?" The caller sees all pending calls in the batch and can approve them together in PR3.

This means authorize must complete for the entire batch before execution begins. For the serial path, this changes the current "authorize → execute per call" to "authorize all → suspend if any / else execute all." For the parallel path, `authorizeBatch` already authorizes all calls serially before execution, so only the suspend check is new.

### D5: Updated Runner flow for [T-TOOLS]

Current flow (simplified):
```
for each call: resolve → authorize → execute
```

New flow:
```
1. authorizeBatch: resolve + authorize all calls (serial, in call order)
   - if any call not found → ErrToolNotFound (fail-fast, no change)
   - if any call denied → ToolDeniedError (fail-fast, no change; does NOT continue to authorize remaining calls)
   - if any call suspended → collect into pending list (authorize continues for remaining calls to check for denies)
2. suspend check: if pending list non-empty → [T-SUSPEND] halt (no tools execute)
3. execute all authorized calls (serial or parallel, no change)
```

This is a structural change only for the serial path. The parallel path already separates authorize and execute phases.

For the serial path, the change is:
- Before: `handleToolCall` does resolve + authorize + execute as one unit.
- After: split into `authorizeBatch` (shared with parallel) → suspend check → `executeBatch`.

### D6: Decision.Allow soft-deprecated

The `Allow` field remains on `Decision` for backward compatibility. New code should use `Kind` instead. The migration rule (`DecisionUnspecified` → read `Allow`) ensures zero breakage. `AllowAll` is updated to return `Decision{Kind: DecisionAllow}`.

### D7: Decision method for kind resolution

```go
func (d Decision) ResolvedKind() DecisionKind
```

Returns `d.Kind` if not `DecisionUnspecified`; otherwise maps `Allow: true → DecisionAllow`, `Allow: false → DecisionDeny`. The Runner uses this single method instead of inlining the migration rule.

## Changes by Package

### policy/policy.go

| Change | Detail |
|--------|--------|
| Add `DecisionKind` type | `uint8` with `DecisionUnspecified/Allow/Deny/Suspend` constants |
| Add `Decision.Kind` field | New field, zero value is `DecisionUnspecified` |
| Add `Decision.ResolvedKind()` | Migration helper |
| Keep `Decision.Allow` | Soft-deprecated, migration rule in `ResolvedKind` |
| Update `AllowAll.Authorize` | Return `Decision{Kind: DecisionAllow}` |

### runner/runner.go

| Change | Detail |
|--------|--------|
| Add `PendingToolCall` struct | `Tool`, `Call`, `Reason` |
| Add `Result.Suspended` | `bool` |
| Add `Result.PendingCalls` | `[]PendingToolCall` |
| Split serial `runToolCalls` | authorize all first → suspend check → execute all |
| Update `authorize` method | Return `(Decision, error)` instead of `error`, check `ResolvedKind()` |
| Update `authorizeBatch` | Return `([]Decision, error)`, check for suspend/deny |
| Suspend halt path | Build `Result` with `Suspended: true` and `PendingCalls` from suspended decisions |

### runner/stream.go (if present)

Same suspend logic as `Run`: yield a `FinalMessage` event with the suspended Result, then stop iteration. No `StreamError` event. `OnError` is not triggered.

### docs/spec/loop-semantics.md

| Change | Detail |
|--------|--------|
| §3 [T-TOOLS] | Split authorize into batch-first phase; add `[T-SUSPEND]` transfer rule |
| §4 | Update serial strategy to separate authorize-batch and execute-batch |
| §7 I3 | Update: Result ∈ {Completed, Suspended, error}, mutually exclusive |
| §7 new invariant | After a suspended halt, `PendingCalls` contains exactly the calls whose `ResolvedKind() == DecisionSuspend` |
| §6 | Suspend is not a runtime error; `OnError` does not fire |

## Updated [T-TOOLS] Transfer Rule

```text
[T-TOOLS]    pre: immediately after [T-MODEL] and toolUses(msg) = [c₁ … cₙ], n ≥ 1
              step 1 — authorizeBatch:
                For each cᵢ in call order: resolve → authorize
                - resolve failure ⟶ halt(wrap(ErrToolNotFound, name))
                - ResolvedKind() == DecisionDeny ⟶ halt(ToolDeniedError{Tool, Decision})
                - ResolvedKind() == DecisionSuspend ⟶ collect into pending list, continue to next call
                (suspend does not short-circuit; authorize continues so deny can take precedence)
              step 2 — suspend check:
                If pending list non-empty ⟶ [T-SUSPEND]
              step 3 — executeBatch:
                Execute all calls (serial or parallel per maxConcurrency)
                Append single RoleTool message
                Apply handoffs (last wins)

[T-SUSPEND]  pre: authorizeBatch found ≥1 suspended calls, no denies
             action: none (no tools execute; allowed-but-not-executed calls are also not in PendingCalls)
             result: halt(Result{
                       Suspended: true,
                       PendingCalls: [{Tool, Call, Reason} for each suspended call only],
                       Messages: history (includes the dangling assistant with all tool_uses),
                       LastAgent: agent,
                       LastMode: mode.Name,
                     })
```

## Backward Compatibility

| Scenario | Behavior |
|----------|----------|
| Old Policy returns `Decision{Allow: true}` | `ResolvedKind() == DecisionAllow` via migration rule. No change. |
| Old Policy returns `Decision{Allow: false}` | `ResolvedKind() == DecisionDeny` via migration rule. No change. |
| New Policy returns `Decision{Kind: DecisionAllow}` | Direct match. `Allow` ignored. |
| New Policy returns `Decision{Kind: DecisionSuspend}` | New suspend path. |
| No Policy configured (AllowAll) | Updated to return `Decision{Kind: DecisionAllow}`. Same behavior. |
| Old code reads `decision.Allow` | Still works. `Allow` field is not removed. |

## Out of Scope (PR3+)

- `InputHash` on `PendingToolCall` (PR3: approval binding)
- `SuspendedRun` / `ResumeApproved` (PR3: approved resume)
- Execution tape recording of suspend/resume events (PR4)
- Effect-aware concurrency changes from suspend (PR5)
- Batch partitioning (allowing partial execution when some calls are suspended)

## Test Plan

1. **Unit: DecisionKind resolution** — `ResolvedKind()` returns correct kind for all combinations of `Kind` and `Allow`.
2. **Unit: AllowAll compatibility** — `AllowAll.Authorize` returns `DecisionAllow`.
3. **Unit: Legacy Decision compatibility** — `Decision{Allow: true}` → `ResolvedKind() == DecisionAllow`; `Decision{Allow: false}` → `ResolvedKind() == DecisionDeny`.
4. **Integration: suspend halts with Result** — Policy returns `DecisionSuspend` for one call in a batch → Runner returns `Result{Suspended: true, PendingCalls: [...]}`.
5. **Integration: suspend with no execution** — When any call is suspended, no tool in the batch executes (verified by hook counts).
6. **Integration: OnError not triggered on suspend** — Suspend path does not call `OnError`.
7. **Integration: deny still fail-fast** — If any call is denied (before or alongside a suspend), `ToolDeniedError` is returned, not a suspended Result.
8. **Integration: deny takes precedence over suspend** — Mixed deny + suspend in same batch → `ToolDeniedError`, no suspend.
9. **Integration: suspended Result has correct history** — `Result.Messages` ends with the assistant message containing the `tool_use` blocks.
10. **Integration: parallel path suspend** — `authorizeBatch` in parallel path also suspends on `DecisionSuspend`.
11. **Integration: Allow + Suspend mixed batch** — Batch with call₁=Allow, call₂=Suspend → `Result{Suspended: true}`, `PendingCalls` contains only call₂, `Result.Messages` ends with assistant message containing both `tool_use` blocks, no `tool_result` follows.
12. **Integration: deny fail-fast after refactor** — call₁=Deny → `authorizeBatch` returns `ToolDeniedError`; call₂ is never resolved or authorized (Policy.Authorize not called for call₂). This verifies the serial-to-batch refactor preserves deny fail-fast.
13. **Property: I3 updated** — `Run` output ∈ {Completed Result, Suspended Result, error}, mutually exclusive.
