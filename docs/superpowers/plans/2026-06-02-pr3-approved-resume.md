# PR3: Approved Resume Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a first-class `ResumeApproved` API that executes the original pending tool calls after human approval. A suspended run produces a `SuspendedRun` snapshot; the caller collects approvals and passes them to `ResumeApproved`, which validates, executes approved calls, writes rejections as visible `tool_result` blocks, and continues the ReAct loop.

**Architecture:** Core change in `runner/` only. No new packages. `PendingToolCall` unchanged. New types: `SuspendedRun` (carries `LastAgentName`+`LastMode` once, plain data with no live agent ref, not per-call), `Approval`, `ApprovalError` (with `ErrInvalidApproval` sentinel, NOT `ErrNotSuspended`). New method: `Runner.ResumeApproved`. Existing `WithResumeFromPendingTools` seam is unchanged.

**Tech Stack:** Go 1.23+, standard library only, TDD (red → green → refactor).

---

## Hard Constraints

- Carry over all constraints from `2026-06-02-fino-core-agent-sdk.md`.
- No new packages. Changes limited to `runner/`, `docs/spec/loop-semantics.md`, `docs/design.md`, READMEs, CHANGELOG, and examples.
- No `InputHash` on `PendingToolCall` or `Approval` — tamper detection is a user-level concern.
- No Stream changes — `ResumeApproved` is Run-only.
- No re-authorization — approved calls bypass Policy.
- Do not commit unless explicitly requested.

## Files

```text
runner/runner.go                     (modify: extract shared loop reused by Run and ResumeApproved)
runner/resume.go                     (new: SuspendedRun/Approval/ApprovalError/ResumeApproved/Result.SuspendedRun())
runner/resume_test.go                (new: ResumeApproved tests)
docs/spec/loop-semantics.md          (modify: add [T-RESUME], split §7.2, add I12)
docs/design.md                       (modify: update 人工确认和恢复 section)
docs/superpowers/specs/2026-06-02-approved-resume-design.md (already written)
docs/superpowers/plans/2026-06-02-pr3-approved-resume.md   (this file)
examples/cookbook/hitl_resume/main.go (modify: use DecisionSuspend + ResumeApproved)
CHANGELOG.md                         (modify: add Unreleased section)
README.md                            (modify: add HITL section if absent)
README.zh-CN.md                      (modify: add HITL section if absent)
```

---

## Task 1: Add Types

**Files:**
- Modify: `runner/runner.go`

- [ ] Add `SuspendedRun` (agent/mode context recorded once, not per pending call):

```go
type SuspendedRun struct {
    Messages      []message.Message
    LastAgentName string // plain data; live agent passed back by caller
    LastMode      string
    PendingCalls  []PendingToolCall
}
```

- [ ] Add `Approval`:

```go
type Approval struct {
    CallID   string
    Approved bool
    Reason   string
}
```

- [ ] Add `ApprovalError` with independent sentinel (NOT `ErrNotSuspended`):

```go
var ErrNotSuspended = errors.New("result is not suspended")
var ErrInvalidApproval = errors.New("invalid approval")

type ApprovalError struct {
    Missing   []string
    Unknown   []string
    Duplicate []string
}

func (e *ApprovalError) Error() string
func (e *ApprovalError) Unwrap() error { return ErrInvalidApproval }
```

- [ ] Add `Result.SuspendedRun()`:

```go
func (r *Result) SuspendedRun() (SuspendedRun, error) {
    if !r.Suspended {
        return SuspendedRun{}, ErrNotSuspended
    }
    return SuspendedRun{
        Messages:     r.Messages,
        LastAgentName: r.LastAgent.Name(), // snapshot stores the name only
        LastMode:     r.LastMode,
        PendingCalls: r.PendingCalls,
    }, nil
}
```

- [ ] `PendingToolCall` is NOT modified. No `AgentName`/`ModeName` fields added. Agent/mode context lives on `SuspendedRun` once.

- [ ] `authorizeBatch` is NOT modified. No per-call fields to populate.

- [ ] Run `go test ./runner`. Expected: PASS (no existing assertions change).

---

## Task 2: Write ResumeApproved Tests (Red)

**Files:**
- Create: `runner/resume_test.go`

Write tests that will fail because `ResumeApproved` doesn't exist yet:

- [ ] **Test 1: SuspendedRun extraction from suspended result** — create a suspended result, call `SuspendedRun()`, assert fields match including `LastAgentName`.

- [ ] **Test 2: SuspendedRun from completed result errors** — call `SuspendedRun()` on a non-suspended Result, expect `ErrNotSuspended`.

- [ ] **Test 3: Approve executes tool and continues loop** — Policy suspends `fetch` → approve → `ResumeApproved` executes `fetch`, appends RoleTool, next model turn returns final text.

- [ ] **Test 4: Reject writes error tool_result** — Policy suspends `delete_file` → reject with reason "user denied" → `ResumeApproved` writes `tool_result{IsError: true}`, does NOT execute the tool.

- [ ] **Test 5: Mixed Allow+Suspend resume** — Batch had call₁=Allow(`alpha`), call₂=Suspend(`fetch`). `ResumeApproved` executes `alpha` without approval + `fetch` with approval.

- [ ] **Test 6: Rejection is model-visible** — After reject, next model turn sees `tool_result{IsError: true}` and can adjust. Use a scripted model that responds differently based on tool_result content.

- [ ] **Test 7: No re-authorization** — Policy would deny `fetch` if asked again. `ResumeApproved` with approval still executes it (Policy is NOT called).

- [ ] **Test 8: Missing approval returns ApprovalError** — 2 pending calls, only 1 approval → `ApprovalError{Missing: ["call_2"]}`, no tools execute. Verify `errors.Is(err, ErrInvalidApproval)` is true, `errors.Is(err, ErrNotSuspended)` is false.

- [ ] **Test 9: Unknown CallID returns ApprovalError** — Approval references a CallID not in pending calls → `ApprovalError{Unknown: ["call_99"]}`.

- [ ] **Test 10: Duplicate approval returns ApprovalError** — Two approvals for same CallID → `ApprovalError{Duplicate: ["call_1"]}`.

- [ ] **Test 11: Agent mismatch returns error** — Handoff switches from agent A to agent B, B's tool is suspended. Caller passes agent A to `ResumeApproved` → error (A.Name ≠ B.Name), no tools execute.

- [ ] **Test 12: Handoff then suspend then resume** — Agent A handoffs to B → B's tool `fetch` is suspended → `ResumeApproved` with correct agent B executes `fetch` and continues.

- [ ] **Test 13: Handoff in approved batch** — Handoff tool in resumed batch applies batch-terminal, last-wins.

- [ ] **Test 14: OnError not triggered on validation failure** — Pre-loop validation errors do not fire OnError.

- [ ] **Test 15: OnError triggered on execution error** — Tool execution error during resume triggers OnError once.

- [ ] Run `go test ./runner`. Expected: compilation fails (ResumeApproved not defined).

---

## Task 3: Implement ResumeApproved (Green)

**Files:**
- Modify: `runner/runner.go`

- [ ] Implement `validateSuspendedRun`:

```go
func validateSuspendedRun(suspended SuspendedRun, approvals []Approval) error {
    // 1. Last message must be assistant with tool_use blocks
    // 2. Every PendingCall.Call.ID must appear in tool_uses
    // 3. No duplicate pending calls
    // 4. One approval per pending call
    // 5. No unknown CallIDs in approvals
    // 6. No duplicate approvals
    // Return ApprovalError with details on failure
}
```

- [ ] Implement `Runner.ResumeApproved`:

```go
func (r *Runner) ResumeApproved(
    ctx context.Context,
    a *agent.Agent,
    suspended SuspendedRun,
    approvals []Approval,
    opts ...RunOption,
) (*Result, error) {
    // 0. Validate agent matches snapshot
    if a.Name() != suspended.LastAgentName {
        return nil, fmt.Errorf("%w: passed %q, want %q", ErrResumeAgentMismatch, a.Name(), suspended.LastAgentName)
    }
    // 1. Validate SuspendedRun + approvals
    if err := validateSuspendedRun(suspended, approvals); err != nil {
        return nil, err
    }
    // 2. Prepare run state from suspended snapshot
    //    mode = a.Mode(suspended.LastMode)
    //    history = suspended.Messages
    // 3. Build approval lookup map
    // 4. Execute batch: for each tool_use in call order:
    //    - suspended call: approved → execute; rejected → error result
    //    - allowed call: execute
    // 5. Append single RoleTool message
    // 6. Apply handoffs
    // 7. Continue ReAct loop from next model turn
}
```

The agent check (step 0) is critical: if a handoff switched to agent B before suspend, the caller must pass the live agent B (whose Name() equals `LastAgentName`), not the root agent A. Otherwise `a.Mode(LastMode)` would resolve tools in A's context, likely causing `ErrToolNotFound`.

The key internal helper is `resumeExecuteBatch`, which iterates over the tool_use blocks from the dangling assistant message, resolves each tool from the agent's mode, and executes or writes rejection results based on the approval map.

- [ ] Run `go test ./runner -run Resume`. Expected: PASS (tests from Task 2 now green).

- [ ] Run `go test ./runner`. Expected: ALL PASS (existing tests unaffected).

---

## Task 4: Update Loop Semantics Spec

**Files:**
- Modify: `docs/spec/loop-semantics.md`

- [ ] Add `[T-RESUME]` transfer rule in §3:

```text
[T-RESUME]   pre: ResumeApproved called with valid SuspendedRun + approvals
              step 1 validate: ...
              step 2 executeBatch: ...
              step 3 append: history = SuspendedRun.Messages ++ [ToolResults(...)]
              step 4 continue from [T-MODEL]
```

- [ ] Add invariant I12 in §7:

```text
| I12 | 审批恢复完备 | ResumeApproved 后，suspended 批次中的每个 tool_use 在追加的 RoleTool 消息中都有对应 tool_result |
```

- [ ] Split §7.2 into:
  - §7.2: 安全边界恢复完备 (I10) — unchanged content
  - §7.3: 一等审批恢复 (I12) — new section for ResumeApproved

- [ ] Update §7.2 last paragraph: remove "未来若引入 Suspend / ResumeApproved" since it now exists.

---

## Task 5: Update Design Document

**Files:**
- Modify: `docs/design.md`

- [ ] Update "人工确认和恢复" section: replace "Policy 可以返回拒绝或特殊错误，用户在外层收集确认后重新调用 runner.Run" with `ResumeApproved` API description.

- [ ] Update "恢复（durable continuation）" reference composition: note that `x/recover` stays scoped to safe-boundary + blind resume (`WithResumeFromPendingTools`); approval-gated resume is the first-class `runner.ResumeApproved` working on a `SuspendedRun`, not on `x/recover.Snapshot` (see Task 7: DROPPED).

---

## Task 6: Update Example

**Files:**
- Modify: `examples/cookbook/hitl_resume/main.go`

- [ ] Rewrite to use `DecisionSuspend` + `Result.SuspendedRun()` + `ResumeApproved` instead of the current `gatePolicy{Allow: false}` + `WithResumeFromPendingTools` pattern.

- [ ] The example should:
  1. Use a Policy that returns `DecisionSuspend` for gated tools.
  2. Call `Run`, get a suspended Result.
  3. Extract `SuspendedRun`.
  4. Print the pending calls for human review.
  5. Build `Approval` list (approve or reject).
  6. Call `ResumeApproved` to continue.

- [ ] Run `go run ./examples/cookbook/hitl_resume`. Expected: runs without error.

---

## Task 7: Update x/recover — DROPPED

**Status: dropped (intentional deviation from the original plan).**

The original plan proposed a `Snapshot.ResumeApproved` convenience wrapper. This is
**not** implemented, by design:

- The design's "Changes by Package" list does not include `x/recover`, and
  loop-semantics §7.3 scopes `x/recover` to safe-boundary + blind resume only;
  approval resume is a first-class `runner.ResumeApproved` API working on a
  `SuspendedRun`, not on a `Snapshot`.
- After the `LastAgent → LastAgentName` fix, a `Snapshot` (only `History` + `Mode`)
  carries neither the agent name nor the suspend metadata (which calls were
  suspended vs allowed), so a faithful wrapper is impossible without conflating
  blind and approval resume.

`x/recover` is left unchanged. Callers persist a `SuspendedRun` themselves and call
`runner.ResumeApproved` directly.

---

## Task 8: Update CHANGELOG and READMEs

**Files:**
- Modify: `CHANGELOG.md`
- Modify: `README.md`
- Modify: `README.zh-CN.md`

- [ ] Add Unreleased section to CHANGELOG:

```markdown
## [Unreleased]

### Added

- **Approved resume** — `runner.ResumeApproved` executes the original pending tool
  calls after human approval. A suspended `Result` can be converted to a
  `SuspendedRun` snapshot via `Result.SuspendedRun()`. Callers collect
  `Approval` values (approve/reject per CallID) and pass them to
  `ResumeApproved`, which validates, executes approved calls, writes
  rejections as model-visible `tool_result` blocks, and continues the
  ReAct loop. Rejected calls are NOT executed — their refusal reason is
  visible to the model so it can adjust. Previously-allowed calls in a
  suspended batch are also executed on resume.
- **SuspendedRun** — carries `LastAgentName` and `LastMode` from the suspended
  result so `ResumeApproved` validates the correct agent context (critical
  after handoff: the caller must pass the agent that was active at suspend
  time, not the root agent).
- **ApprovalError** — typed error for validation failures (missing, unknown, or
  duplicate approvals), with `ErrInvalidApproval` sentinel.

### Changed

- **Loop semantics** — `docs/spec/loop-semantics.md` gains the `[T-RESUME]`
  transition and invariant I12 (approval resume completeness), and §7.2
  is split into safe-boundary recovery and approval resume sections.
```

- [ ] Add HITL section to READMEs if absent, linking the `hitl_resume` example.

---

## Task 9: Final Verification

- [ ] Run `gofmt -l .`. Expected: no output.
- [ ] Run `go vet ./...`. Expected: no issues.
- [ ] Run `go test ./... -race -count=1`. Expected: ALL PASS.
- [ ] Run `go list -deps ./message ./tool ./model ./agent ./policy ./hooks ./runner | grep -E '/x/' && echo "LEAK" || echo "clean"`. Expected: `clean`.
- [ ] Verify no new external dependencies: `go mod graph`. Expected: unchanged.
- [ ] Verify forbidden paths still absent: `graph/`, `rag/`, `session/`, `mcp/`, `tools/filesystem/`, `tools/bash/`.
- [ ] Review diff: `git diff main --stat`.

---

## Self-Review Checklist

- `ResumeApproved` executes the ORIGINAL tool call, not a regenerated one.
- Rejections are model-visible `tool_result` blocks, not hidden control flow.
- No re-authorization — Policy is not called on resume.
- `WithResumeFromPendingTools` still works unchanged (backward compatible).
- No `InputHash` — tamper detection is a user-level concern.
- No Stream changes — `ResumeApproved` is Run-only.
- `ApprovalError.Unwrap()` returns `ErrInvalidApproval`, NOT `ErrNotSuspended`.
- `SuspendedRun` carries `LastAgentName` + `LastMode` once (plain data, no live agent ref; JSON-marshal depends on tool metadata); `PendingToolCall` is NOT extended.
- Agent mismatch (handoff before suspend) is validated and returns an error.
- Loop semantics spec updated with `[T-RESUME]` and I12.
- Example updated to use `DecisionSuspend` + `ResumeApproved`.
- No new packages, no new external dependencies.
