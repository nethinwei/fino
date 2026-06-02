# fino Sufficiency & Formal Semantics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Prove the sufficiency thesis — *reliable complex agent capability needs no framework, only correct minimal primitives, precise semantics, and composition* — without adding any new capability to the core. Deliver (1) a formal loop spec, (2) property-based invariant tests over the existing Runner, and (3) reference compositions under `x/` that constructively prove the seams suffice.

**Non-Goal:** New core features. This version adds zero capabilities to `message`, `tool`, `model`, `agent`, `policy`, `hooks`, `runner`. The only permitted core change is exposing a missing seam, and only when a reference composition proves the gap (see Task 5).

**Tech Stack:** Go 1.23+, standard library only everywhere (core and `x/`). Property-based tests use stdlib `testing/quick` plus hand-written generators. Module path `github.com/nethinwei/fino`.

---

## Hard Constraints

- Carry over all constraints from `2026-06-02-fino-core-agent-sdk.md`.
- No new capability in core packages. Diffs to `runner/`, `agent/`, `model/`, `tool/`, `message/`, `policy/`, `hooks/` are forbidden except the seam in Task 5, which is gated on a failing reference composition.
- `x/` add-ons: standard-library only; may import core; MUST NOT be imported by core. Verify with `go list`.
- No external test dependency. Property testing uses `testing/quick` and custom generators only.
- No graph state machine, no checkpoint subsystem. Recovery serializes only `(pending tool calls, history, mode)`.
- The spec `docs/spec/loop-semantics.md` is normative. If implementation and spec disagree, fix implementation, not spec.
- Do not commit unless explicitly requested.

## Files

```text
docs/spec/loop-semantics.md          (exists; keep authoritative)
docs/design.md                       (exists; thesis sections added)
runner/model_state_test.go           (new: reference state machine + scripted model/tools harness)
runner/invariants_test.go            (new: property-based checks for I1–I9)
runner/recover_seam_test.go          (new: probe for I10 seam gap; drives Task 5 decision)
x/replay/replay.go                   (new)
x/replay/replay_test.go              (new)
x/recover/recover.go                 (new)
x/recover/recover_test.go            (new)
x/budget/budget.go                   (new)
x/budget/budget_test.go              (new)
x/trace/trace.go                     (new)
x/trace/trace_test.go                (new)
x/eval/eval.go                       (new)
x/eval/eval_test.go                  (new)
README.md / README.zh-CN.md          (modify: add "Sufficiency" section linking spec + x/)
```

---

## Task 1: Reference State Machine Harness

Build the model the property tests check against. This is a pure-Go reimplementation of §3–§4 semantics, deliberately independent of `runner.go`, used as the oracle.

**Files:**
- Create: `runner/model_state_test.go`

- [ ] Define a deterministic scripted model and scripted tools that record call order, so a run is fully reproducible from a script:

```go
// scriptedModel returns pre-baked responses in order; both Generate and Stream
// share the same script so Run and Stream are checked against one oracle.
type scriptedModel struct {
    responses []message.Message
    seenInput [][]message.Message
}
```

- [ ] Define a pure reference interpreter `refRun(script) refOutcome` that applies `[T-MODEL] / [T-FINAL] / [T-TOOLS] / [T-MAXTURNS]` per the spec and returns: final `history`, terminal error (if any), last agent/mode, and the ordered event list. No goroutines.

- [ ] Define generators producing random but valid scripts: variable tool-call batch sizes, interleaved handoffs, denial/missing-tool/cancel injections, and `maxConcurrency ∈ {1, 2, 4}`.

- [ ] Run `go test ./runner`.

Expected: PASS (harness compiles; no assertions yet beyond a smoke test that `refRun` halts).

---

## Task 2: Property-Based Invariants I1–I8

Check the real Runner against the reference interpreter and the spec invariants.

**Files:**
- Create: `runner/invariants_test.go`

- [ ] For each generated script, run the real `Runner.Run` and assert:
  - **I1 结果有序**: every appended `RoleTool` message has `tool_result[i].ToolUseID == tool_use[i].ID`.
  - **I2 批次单消息**: count of `RoleTool` messages == count of tool-bearing turns.
  - **I3 终止唯一**: exactly one of `(Result, error)` is non-nil.
  - **I4 OnError 一次**: a counting hook observes exactly one `OnError` for terminating-error scripts, zero for success scripts.
  - **I5 授权先于执行**: a Policy + tool that append to a shared ordered log assert every `Run` is preceded by its `Authorize=Allow`.
  - **I7 handoff 末位**: `Result.LastAgent` == target of the last handoff in the final batch.
  - **I8 无系统泄漏**: `∀ msg ∈ Result.Messages: msg.Role != RoleSystem`.

- [ ] Use `testing/quick.Check` with a custom `Generate` on a `script` type, or an explicit loop over N random seeds (default 1000) with `-count` friendliness.

- [ ] Add a parallel-equivalence property: for each script, `Run` with `maxConcurrency=1` and `maxConcurrency=4` produce identical `Result.Messages` (order-independent tools) — establishes **I6 fail-fast 等价** plus result-order equivalence.

- [ ] Run `go test ./runner -run Invariants -count=1`.

Expected: PASS. If any property fails, the bug is in `runner.go` or the spec; fix per §8 consistency obligation, not by weakening the test.

---

## Task 3: Stream Invariants I3, I4, I9 + Cancellation

**Files:**
- Modify: `runner/invariants_test.go`

- [ ] For each script, drive `Runner.Stream` and assert:
  - Single goroutine: events arrive on the caller's range loop only (no data race; run under `-race`).
  - **I3**: at most one terminal `FinalMessage`; any terminal error yields exactly one `StreamError` paired with a non-nil second value, after which iteration stops.
  - **I4**: `OnError` fires exactly once per terminal error (note construction-time errors fire OnError on Stream but not Run — assert both paths per §6).
  - Event order: `ToolCall` in call order, `ToolResult` in completion order, `Handoff` after batch.

- [ ] **I9 ctx 忠实**: inject a context cancelled mid-run; assert no `model`/`tool` call is observed after cancellation via instrumented fakes. Test both serial and parallel batches.

- [ ] Run `go test ./runner -race -run 'Invariants|Stream' -count=1`.

Expected: PASS under `-race`.

---

## Task 4: `x/replay` — Record & Replay (proves the effect-seam)

**Files:**
- Create: `x/replay/replay.go`, `x/replay/replay_test.go`

- [ ] Implement, stdlib only:

```go
type Log struct { /* ordered records: model responses + tool outputs */ }

type RecordingModel struct { Next model.Model; Log *Log } // implements model.Model
type ReplayModel    struct { Log *Log }                   // implements model.Model; calls no provider

func RecordingTool(t tool.Tool, log *Log) tool.Tool       // wraps a tool, records Result
func ReplayTool(name string, log *Log) tool.Tool          // serves recorded Result
```

- [ ] `Log` is JSON-serializable (uses `message.Message` / `tool.Result`, which are already flat-JSON friendly).

- [ ] Test: run an agent once with `RecordingModel` + recording tools over a fake provider; persist `Log` to JSON; re-run with `ReplayModel` + replay tools; assert identical `Result.Messages`.

- [ ] Test: `ReplayModel` never touches the network (use a `Next` that panics if called).

- [ ] Run `go test ./x/replay -count=1`.

Expected: PASS. This is the constructive proof that all non-determinism flows through `model.Model` + `tool.Tool`.

---

## Task 5: `x/recover` — Durable Continuation + Seam Decision (I10)

This task exercises the seam discipline explicitly.

**Files:**
- Create: `runner/recover_seam_test.go` (probe)
- Create: `x/recover/recover.go`, `x/recover/recover_test.go`

- [ ] **Probe first** in `runner/recover_seam_test.go`: construct a run that pauses *before* tool execution (Policy denies one call, run halts with `ToolDeniedError`). Capture `Result.Messages` is unavailable on error — capture history via a hook. Attempt to resume by calling `Run` again with that history (last msg is assistant with unfulfilled `tool_use`).

- [ ] Record the outcome and decide via the discipline tree:

```text
CASE A: resume works using only existing API (e.g., crash mid-run where history ends
        in a tool_result or assistant text, re-Run continues correctly).
        ⟶ implement x/recover with zero core change.

CASE B: HITL resume (history ends in assistant tool_use, no results) cannot be expressed,
        because [T-MODEL] always calls the model first and never executes pending tools.
        ⟶ MISSING SEAM. Add the MINIMAL seam, not the capability:
           Option B1 (preferred): runner.WithResumeFromPendingTools() RunOption that, when
             history ends in an assistant message with unfulfilled tool_use, runs [T-TOOLS]
             on those pending calls before the next [T-MODEL]. Pure entry-point behavior;
             no new state, no checkpoint type.
           Option B2: expose pending calls explicitly on a typed error / Result field so the
             user re-injects results themselves.
        Update docs/spec/loop-semantics.md §7.1 with the chosen transition. Get explicit
        sign-off before editing runner.go.
```

- [ ] Implement `x/recover`, stdlib only:

```go
type Snapshot struct {
    History []message.Message
    Mode    string
    Pending []message.ToolUse // empty unless paused before execution
}

func Capture(res *runner.Result) Snapshot                 // crash-safe path
func CaptureFromHistory(history []message.Message, mode string) Snapshot
func (s Snapshot) Resume(ctx, r *runner.Runner, a *agent.Agent) (*runner.Result, error)
```

- [ ] Test CASE A (crash recovery): mid-run snapshot → `Resume` produces the same final result as an uninterrupted run.

- [ ] Test the HITL path consistent with the Task-5 decision (B1 or B2). Assert no graph/checkpoint type is introduced; `Snapshot` holds only the three serialized things.

- [ ] Run `go test ./runner ./x/recover -count=1`.

Expected: PASS. Any `runner.go` change is limited to the agreed minimal seam and is reflected in the spec.

---

## Task 6: `x/budget`, `x/trace`, `x/eval` (cross-cutting + observability + eval)

**Files:**
- Create: `x/budget/budget.go` (+ test)
- Create: `x/trace/trace.go` (+ test)
- Create: `x/eval/eval.go` (+ test)

- [ ] `x/budget`: a `model.Model` decorator accumulating a user-supplied cost function; returns a typed `ErrBudgetExceeded` when over limit, which the Runner surfaces as a run-time terminating error. Test: a scripted run trips the budget at the expected turn and `OnError` fires once (cross-check with I4).

- [ ] `x/trace`: a tiny stdlib `Tracer` interface and a `hooks.Hooks` constructor that opens/closes spans on Before/After and records the terminal error on `OnError`. No otel dependency — provide an otel adapter only as an example, not in `x/`. Test: span open/close counts and ordering match the deterministic hook firing.

- [ ] `x/eval`: built on `x/replay`. Given a recorded `Log` and an assertion over final `Result.Messages` / event sequence, run a reproducible regression. Test: a stored fixture replays to a stable assertion; mutating the fixture fails the eval.

- [ ] Run `go test ./x/... -count=1`.

Expected: PASS.

---

## Task 7: Boundary & Dependency Verification

- [ ] Verify `x/` is never imported by core:

```bash
go list -deps ./message ./tool ./model ./agent ./policy ./hooks ./runner | grep -E '/x/' && echo "LEAK" || echo "clean"
```

Expected: `clean`.

- [ ] Verify core remains stdlib-only (no new module requires):

```bash
go mod graph
```

Expected: no external dependency introduced.

- [ ] Verify forbidden core paths still absent: `graph/`, `rag/`, `session/`, `mcp/`, `tools/filesystem/`, `tools/bash/`, and no checkpoint type in `runner/`.

- [ ] `git diff --stat` on core packages shows either no change or only the agreed Task-5 seam.

---

## Task 8: Docs & README

- [ ] Confirm `docs/design.md` thesis sections and `docs/spec/loop-semantics.md` are consistent with final code (especially any Task-5 seam).

- [ ] Add a "Sufficiency" section to `README.md` and `README.zh-CN.md`: one paragraph stating the thesis, linking the spec and `x/` reference compositions as constructive evidence.

- [ ] Run `gofmt -w .` and `go test ./... -race -count=1`.

Expected: PASS.

---

## Self-Review Checklist

- Formal spec exists and is marked normative; invariants I1–I10 enumerated.
- Property-based tests cover I1–I9 over random scripts and both concurrency settings.
- Parallel/serial observable equivalence is a property, not a single case.
- `x/replay` proves all non-determinism flows through `model.Model` + `tool.Tool`.
- `x/recover` holds only `(pending, history, mode)`; no checkpoint/graph type.
- Task-5 seam decision is documented; any `runner.go` change is minimal and mirrored in the spec.
- `x/budget` error path cross-checks I4 (OnError once).
- `go list` proves `x/` is not imported by core; core stays stdlib-only.
- No new core capability; forbidden paths still absent.
