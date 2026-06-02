# PR5: Effect-Aware Concurrency Design

## Goal

Make `runner.WithMaxConcurrency` honor `tool.Effects.ParallelSafe` by default.
When a caller opts into concurrent tool execution with `WithMaxConcurrency(n >
1)`, the Runner may execute a tool-call batch concurrently only when every tool
in that batch has explicitly declared `Effects.ParallelSafe == true`.

PR5 is a scheduling refinement over the existing runner paths. It must not add a
graph engine, session store, retry system, idempotency layer, or a new core
abstraction.

## Current State

`WithMaxConcurrency(n > 1)` currently enables parallel execution for any batch
with more than one tool call. The Runner already preserves the protocol-level
properties needed for parallel execution:

- It authorizes the whole batch before executing any tool.
- A deny or suspend anywhere in the batch executes no tools.
- Parallel results are written back in call order.
- Handoffs apply at batch end in call order.
- Fail-fast cancellation reports the first genuine error by call order.

The missing piece is external-state safety. `tool.Effects` exists, but the
Runner does not yet read it. Therefore zero-value tools can run concurrently
today even though zero-value `Effects` means unspecified and conservative.

## Design Decision

Use the simplest possible rule: scheduling reads only `ParallelSafe`.

```text
WithMaxConcurrency(n <= 1): serial
WithMaxConcurrency(n > 1) and batch size <= 1: serial
WithMaxConcurrency(n > 1) and every selected tool declares ParallelSafe: parallel
WithMaxConcurrency(n > 1) and any selected tool lacks ParallelSafe: serial
```

The Runner does not infer safety from `ReadOnly`, `Idempotent`,
`ExternalWrite`, or `Destructive`. Those fields remain available to policies,
tools, and future contracts, but PR5 does not combine them into scheduler logic.

This keeps the contract explicit:

- Zero-value `Effects` is conservative and serial.
- `ParallelSafe == true` is the only opt-in to concurrent batch execution.
- Destructive tools should remain serial by not declaring `ParallelSafe`.
- If a tool author declares a destructive tool as `ParallelSafe`, the Runner
  trusts that explicit declaration instead of second-guessing it.

## Architecture

Keep one scheduler decision in `runner`, after the batch has been resolved and
authorized.

The normal `Run` flow becomes:

```text
runToolCalls
  collectTools
  authorizeBatch              // serial, existing behavior
  if pending: suspend         // existing behavior
  if batchParallelSafe: runToolCallsParallel-equivalent path
  else: executeBatchSerial
```

The `Stream` flow mirrors the same decision:

```text
streamToolCalls
  collectTools
  authorizeBatch              // serial, existing behavior
  if pending: downgrade suspend to ToolDeniedError
  if batchParallelSafe: dispatchTools/drainToolResults/finalizeStreamBatch
  else: existing serial stream path
```

The helper can be small and internal, for example:

```go
func (r *Runner) shouldRunBatchParallel(selected []tool.Tool) bool
```

It returns true only when `r.maxConcurrency > 1`, the batch has more than one
selected tool, and every selected tool reports `Info().Effects.ParallelSafe`.

No new public option is needed. `WithMaxConcurrency` remains the caller's
concurrency cap, not a guarantee that every batch will run concurrently.

## Behavior

### Run

`Run` keeps all existing denial, suspension, ordering, handoff, hook, and error
semantics.

The only behavior change is that an opted-in parallel runner will fall back to
serial execution for any batch containing a tool whose `ParallelSafe` declaration
is absent or false.

### Stream

`Stream` uses the same effect gate. When the gate allows parallel execution,
ToolCall events still emit in call order and ToolResult events still emit in
completion order. When the gate falls back to serial execution, Stream uses the
existing serial event behavior.

`DecisionSuspend` remains unchanged in Stream: it downgrades to a
`ToolDeniedError` because Stream has no suspended `Result` path.

### ResumeApproved

`ResumeApproved` remains serial. Approval resume is about executing an exact
previously suspended batch after human decisions. PR5 should not change that
path or add effect-aware parallel resume.

## Documentation Changes

- Update `docs/spec/loop-semantics.md` §4.2 so parallel execution is conditioned
  on all selected tools declaring `ParallelSafe`.
- Update invariant I6 from a future assumption to the implemented rule: protocol
  equivalence is required only for batches that pass the explicit
  `ParallelSafe` gate.
- Update the `WithMaxConcurrency` comment to say it sets an upper bound; actual
  parallel execution also requires a fully `ParallelSafe` batch.
- Update `docs/roadmap.md` to mark PR5 delivered once implementation and tests
  are complete.

## Tests

PR5 should pin both the new gate and the existing behavior it preserves.

1. A batch where every tool declares `ParallelSafe` executes concurrently under
   `WithMaxConcurrency(n > 1)`.
2. A batch with any zero-value or non-`ParallelSafe` tool executes serially even
   under `WithMaxConcurrency(n > 1)`.
3. Existing parallel behavior tests explicitly mark tools as `ParallelSafe` when
   they intend to exercise the parallel path.
4. Bounded concurrency still caps the number of concurrently running safe tools.
5. Result ordering, fail-fast error selection, and batch-terminal handoff remain
   unchanged for safe batches.
6. `Stream` applies the same effect gate.
7. Suspend and deny tests continue to prove that no tool executes when a batch
   suspends or is denied.

## Non-Goals

- No batch partitioning. A mixed safe/unsafe batch runs entirely serially.
- No automatic inference from `ReadOnly` or `Idempotent`.
- No special-case scheduler rule for `Destructive` or `ExternalWrite`.
- No automatic retries, idempotency keys, or exactly-once guarantees. Those are
  PR6 scope.
- No stream suspend event.
- No change to `ResumeApproved` execution order.

## Self-Review

- Placeholder scan: none.
- Internal consistency: the scheduler has a single gate, `ParallelSafe`, and all
  sections use that same rule.
- Scope check: one runner scheduling refinement plus tests and docs; suitable for
  a single PR.
- Ambiguity check: `WithMaxConcurrency` is an upper bound, not a force-parallel
  switch; zero-value effects are serial.
