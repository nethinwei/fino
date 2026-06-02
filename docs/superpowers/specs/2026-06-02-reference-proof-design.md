# PR7: Reference Proof — Safe Coding Agent Example

## Status

Draft

## Summary

Build a small safe coding-agent flow in `examples/coding_agent/` that proves all
core mechanisms work end-to-end: plan/code dual-mode, policy-driven approval,
suspend/resume, effect-aware concurrency, idempotency boundary, and replay
equivalence. No new core or x/ package is added.

## Scope

This is a reference proof, not a product. It demonstrates that fino's public
APIs compose correctly for a realistic agent workflow. It does not implement a
full IDE, sandbox, MCP, session persistence, or graph orchestration.

**Safety boundary.** "Safe" here means exactly one thing: irreversible writes
(`write_file`) are gated by human approval through the Policy suspend/resume
path. It does **not** mean sandboxed or trustworthy execution — `run_tests`
shells out to `go test`, which compiles and runs arbitrary project code without
approval. Execution isolation is out of scope (see below); the proof's safety
claim is scoped to the write-approval boundary only.

## Agent and Mode Structure

One `coding` agent with two modes:

| Mode   | Tools                               | Purpose                    |
|--------|-------------------------------------|----------------------------|
| plan   | read_file, list_files               | Inspect project, no writes |
| code   | read_file, list_files, write_file, run_tests | Propose and verify changes |

Plan mode is the default. The CLI drives plan first, then code.

## Tool Definitions

All tools built with `tool.NewFunc`. Effects are declared explicitly.

### read_file

- Input: `{ "path": string }`
- Output: file content as string
- Effects: `ReadOnly=true, ParallelSafe=true`

### list_files

- Input: `{ "path": string }`
- Output: newline-separated file list
- Effects: `ReadOnly=true, ParallelSafe=true`

### write_file

- Input: `{ "path": string, "content": string }`
- Output: `"wrote <path>"`
- Effects: `Destructive=true, ExternalWrite=true, RequiresApproval=true`

The tool writes full content (no diff). CLI displays the pending call's input
(path + content summary) for human review.

### run_tests

- Input: `{ "path": string }`
- Output: `go test` output
- Effects: `ExternalWrite=true`

Not `RequiresApproval`: a test *failure* has no irreversible side effect. Note
this is a deliberate, narrow choice for the proof — `go test` itself runs
arbitrary code, so this tool is trusted, not sandboxed (see Safety boundary).

## Policy

`approvalPolicy`: suspend any tool whose `Effects.RequiresApproval` is true,
allow everything else.

```go
type approvalPolicy struct{}

func (approvalPolicy) Authorize(_ context.Context, req policy.Request) (policy.Decision, error) {
    if req.Tool.Effects.RequiresApproval {
        return policy.Decision{Kind: policy.DecisionSuspend, Reason: "requires human approval"}, nil
    }
    return policy.Decision{Kind: policy.DecisionAllow}, nil
}
```

This is the simplest policy that proves the suspend/approve/resume path. No
RBAC, risk scoring, or audit logging.

## CLI Main Loop

The Runner is constructed once with `runner.New(model, WithPolicy(approvalPolicy{}), WithMaxConcurrency(2))`
so the effect-aware concurrency path is actually enabled (see Effect-Aware
Concurrency below).

1. **Plan phase**: `runner.Run(ctx, agent, Text(prompt), WithMode("plan"))`
   - Print the plan text.
   - Record the plan run into the shared Log.

2. **Code phase**: `runner.Run(ctx, agent, Messages(planResult.Messages), WithMode("code"), WithRunID(runID))`
   - If `result.Suspended`:
     a. `suspended, _ := result.SuspendedRun()`, then `RecordSuspend(log, suspended)`
        immediately, before any human interaction (`RecordSuspend` takes a
        `runner.SuspendedRun`, not a `*Result`).
     b. Display pending `write_file` calls (path + content summary).
     c. Prompt user for y/n approval.
     d. Build `[]runner.Approval`.
     e. `RecordApproval(log, approvals)`.
     f. `result, err = runner.ResumeApproved(ctx, agent, suspended, approvals)`.
     g. `RecordResume(log, suspended, approvals, result, err)`.
     h. Loop back to check for further suspensions.
   - If completed: print final result.

3. **Recording**: `RecordingModel` + `RecordingTool` + `RecordingPolicy` wrap
   all seams. `RecordSuspend`, `RecordApproval`, `RecordResume`,
   `RecordTermination` capture run boundaries.

Key details:
- Plan messages carry forward to code phase via `Messages(planResult.Messages)`.
- `WithRunID` ensures `ExecutionContext.IdempotencyKey` is populated; `SuspendedRun.RunID` restores it on resume (I13).
- The approval loop handles multiple suspend/resume cycles (e.g., model proposes
  a second write after the first is approved).

## Effect-Aware Concurrency

To actually prove PR5 (not just declare `ParallelSafe` inertly), two conditions
must hold and both are pinned by the replay test:

1. The Runner is built with `WithMaxConcurrency(2)`.
2. The recorded scenario includes at least one turn whose tool batch is two
   `read_file` calls (both `ParallelSafe=true`), so the batch takes the parallel
   path. A mixed batch (e.g. `read_file` + `run_tests`) would fall back to
   serial, which would not exercise PR5.

The plan phase is the natural place for this: a plan turn that reads two files
at once.

## Replay Test

Location: `examples/coding_agent/replay_test.go` (external test package
`coding_agent_test`).

`eval.RunWithOptions` drives a single `runner.Run` against the agent's default
mode and has no suspend/resume or mode-switch handling, so it cannot express the
plan → code → suspend → resume orchestration this proof records. The replay test
therefore drives the Runner directly with `ReplayModel` + `ReplayTool` +
`ReplayPolicy`, mirroring `main.go`'s control flow (the same direct-drive pattern
used by `x/replay/tape_flow_test.go`). No new `x/eval` capability is added.

1. Load the fixture `testdata/plan_code_suspend_resume.json` with `replay.Unmarshal`.
2. Build the `coding` agent with `ReplayTool`-backed tools sharing the Log.
3. Construct the Runner with `ReplayModel`, `WithPolicy(&replay.ReplayPolicy{Log: log})`,
   and `WithMaxConcurrency(2)` (same options as `main.go`).
4. Replay the plan phase: `Run(..., WithMode("plan"))`; assert the plan text.
5. Replay the code phase: `Run(Messages(planResult.Messages), WithMode("code"), WithRunID(runID))`;
   expect `Suspended`.
6. Replay the approval loop: derive approvals from the recorded tape, call
   `ResumeApproved`, repeat until the run completes.
7. Assert the final text matches and the suspend count matches the recorded tape.

No API key, no filesystem, no real `go test`, fully deterministic.

## File Structure

```
examples/coding_agent/
├── main.go          # CLI entry: plan → code → approval loop + recording
├── tools.go         # Tool constructors (read_file, list_files, write_file, run_tests)
├── replay_test.go   # Replay equivalence test (package coding_agent_test)
└── testdata/
    └── plan_code_suspend_resume.json  # Pre-recorded Log fixture
```

Estimated ~400 lines total. No new core package, no new x/ package.

## Success Criteria

- [ ] `DEEPSEEK_API_KEY=sk-... go run ./examples/coding_agent` completes a
      plan → code → approve → write → test cycle against a real project.
- [ ] `go test ./examples/coding_agent` passes without any API key (replay only).
- [ ] `go vet ./...` and `gofmt -l .` clean.
- [ ] The example uses every core mechanism: dual-mode, Effects, Policy
      suspend/approve/resume, effect-aware concurrency (`WithMaxConcurrency(2)`
      plus a recorded all-`ParallelSafe` batch that takes the parallel path),
      ExecutionContext/WithRunID, RecordingModel/Tool/Policy, and replay
      equivalence.
- [ ] The replay test reproduces the full plan → code → suspend → resume cycle
      by driving the Runner directly (not `eval.RunWithOptions`), and asserts the
      recorded suspend count.

## Out of Scope

- Diff-based write_file (full content write is sufficient for the proof).
- Sandboxed execution.
- MCP integration.
- Session persistence.
- Graph orchestration or multi-agent handoff.
- Streaming UI or interactive TUI.
