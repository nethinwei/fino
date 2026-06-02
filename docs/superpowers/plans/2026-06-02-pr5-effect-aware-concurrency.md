# PR5: Effect-Aware Concurrency Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make `runner.WithMaxConcurrency` execute a tool-call batch concurrently only when every selected tool declares `tool.Effects.ParallelSafe`.

**Architecture:** Keep the existing serial and parallel runner paths. Add one internal scheduler gate after `authorizeBatch` resolves the selected tools. `WithMaxConcurrency` remains an upper bound; zero-value effects remain conservative and serial.

**Tech Stack:** Go 1.23+, standard library only, TDD (red -> green -> refactor).

---

## Hard Constraints

- Do not add a new public option or abstraction.
- Do not infer concurrency safety from `ReadOnly`, `Idempotent`, `ExternalWrite`, or `Destructive`.
- Do not partition mixed batches. One unsafe tool makes the whole batch serial.
- Do not change `ResumeApproved`; it stays serial.
- Do not add retries, idempotency keys, persistence, graph execution, or session state.
- Do not commit unless the user explicitly requests it.

## Files

```text
runner/runner.go                     (modify: add effect-aware scheduling gate for Run)
runner/runner_stream.go              (modify: use the same gate for Stream)
runner/runner_parallel_test.go       (modify/add: ParallelSafe red tests and update parallel-path tests)
runner/model_state_test.go           (modify if needed: keep prop harness safe tools explicitly ParallelSafe)
docs/spec/loop-semantics.md          (modify: make §4.2 and I6 effect-aware)
docs/roadmap.md                      (modify: mark PR5 delivered after implementation)
docs/superpowers/specs/2026-06-02-effect-aware-concurrency-design.md (already written)
docs/superpowers/plans/2026-06-02-pr5-effect-aware-concurrency.md   (this file)
```

---

## Task 1: Write Effect Gate Tests (Red)

**Files:**
- Modify: `runner/runner_parallel_test.go`

- [ ] Add a helper near the existing parallel tests to create explicitly safe function tools:

```go
func parallelSafeTool(t *testing.T, name, description string, fn func(context.Context, echoInput) (string, error)) tool.Tool {
	t.Helper()
	tl, err := tool.NewFunc(name, description, fn,
		tool.WithEffects(tool.Effects{ParallelSafe: true}),
	)
	if err != nil {
		t.Fatalf("NewFunc %q error: %v", name, err)
	}
	return tl
}
```

- [ ] Add a test proving zero-value tools stay serial even under `WithMaxConcurrency(2)`. The first tool waits on a channel. The second tool records whether it started before the first tool was released. In the correct implementation, it must not start early.

```go
func TestRunMaxConcurrencyFallsBackToSerialForUnspecifiedEffects(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondStarted := make(chan struct{}, 1)

	first, _ := tool.NewFunc("first", "first", func(ctx context.Context, in echoInput) (string, error) {
		close(firstStarted)
		<-releaseFirst
		return "first", nil
	})
	second, _ := tool.NewFunc("second", "second", func(ctx context.Context, in echoInput) (string, error) {
		secondStarted <- struct{}{}
		return "second", nil
	})

	m := batchThenFinal([]message.Block{toolUse("c0", "first"), toolUse("c1", "second")}, "done")
	r, err := New(m, WithMaxConcurrency(2))
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, runErr := r.Run(context.Background(), testAgent(t, first, second), Text("hi"))
		done <- runErr
	}()

	<-firstStarted
	select {
	case <-secondStarted:
		t.Fatal("second tool started before first completed; zero-value effects must force serial execution")
	case <-time.After(20 * time.Millisecond):
	}

	close(releaseFirst)
	if err := <-done; err != nil {
		t.Fatalf("Run error: %v", err)
	}
}
```

- [ ] Add a test proving a mixed batch stays serial when one tool lacks `ParallelSafe`:

```go
func TestRunMaxConcurrencyFallsBackToSerialForMixedParallelSafety(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondStarted := make(chan struct{}, 1)

	first := parallelSafeTool(t, "first", "first", func(ctx context.Context, in echoInput) (string, error) {
		close(firstStarted)
		<-releaseFirst
		return "first", nil
	})
	second, _ := tool.NewFunc("second", "second", func(ctx context.Context, in echoInput) (string, error) {
		secondStarted <- struct{}{}
		return "second", nil
	})

	m := batchThenFinal([]message.Block{toolUse("c0", "first"), toolUse("c1", "second")}, "done")
	r, err := New(m, WithMaxConcurrency(2))
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, runErr := r.Run(context.Background(), testAgent(t, first, second), Text("hi"))
		done <- runErr
	}()

	<-firstStarted
	select {
	case <-secondStarted:
		t.Fatal("mixed safe/unsafe batch ran concurrently; whole batch must be serial")
	case <-time.After(20 * time.Millisecond):
	}

	close(releaseFirst)
	if err := <-done; err != nil {
		t.Fatalf("Run error: %v", err)
	}
}
```

- [ ] Update `TestRunParallelExecutesConcurrently`, `TestRunParallelPreservesResultOrder`, `TestRunParallelBoundedConcurrency`, `TestRunParallelToolErrorFailFast`, `TestRunParallelHandoffLastWins`, and `TestStreamParallelEventsAndBatch` to create `ParallelSafe` tools when they are intended to exercise the parallel path.

- [ ] Run the new red test:

```bash
go test -run TestRunMaxConcurrencyFallsBackToSerialForUnspecifiedEffects ./runner
```

Expected before implementation: FAIL because the second zero-value tool starts before the first completes.

---

## Task 2: Implement Run Gate (Green)

**Files:**
- Modify: `runner/runner.go`

- [ ] Update `WithMaxConcurrency` documentation:

```go
// WithMaxConcurrency sets the maximum number of tools the Runner may execute
// concurrently within a single tool-call batch. A value of n <= 1 (the default)
// keeps tools serial. A value of n > 1 permits up to n tools at once only when
// every tool in the batch declares tool.Effects.ParallelSafe; otherwise the
// whole batch falls back to serial execution. The Runner still authorizes all
// calls serially in call order, preserves result order, and is fail-fast on the
// first error.
func WithMaxConcurrency(n int) Option { return func(r *Runner) { r.maxConcurrency = n } }
```

- [ ] Add the internal helper near the parallel batch helpers:

```go
func (r *Runner) shouldRunBatchParallel(selected []tool.Tool) bool {
	if r.maxConcurrency <= 1 || len(selected) <= 1 {
		return false
	}
	for _, t := range selected {
		if t == nil || !t.Info().Effects.ParallelSafe {
			return false
		}
	}
	return true
}
```

- [ ] Change `runToolCalls` so it authorizes first, then chooses serial or parallel. Replace the early `if r.maxConcurrency > 1 && len(calls) > 1` delegation with:

```go
func (r *Runner) runToolCalls(ctx context.Context, st *runState, calls []message.ToolUse) (context.Context, []PendingToolCall, error) {
	_, toolsByName := collectTools(st.mode.Tools)
	selected, pending, err := r.authorizeBatch(ctx, st, toolsByName, calls)
	if err != nil {
		r.onError(ctx, err)
		return ctx, nil, err
	}
	if len(pending) > 0 {
		return ctx, pending, nil
	}
	if r.shouldRunBatchParallel(selected) {
		return r.finishToolCallsParallel(ctx, st, selected, calls)
	}
	ctx, blocks, err := r.executeBatchSerial(ctx, st, selected, calls)
	if err != nil {
		return ctx, nil, err
	}
	if err := st.applyHandoffs(selected); err != nil {
		return ctx, nil, err
	}
	st.history = append(st.history, message.ToolResults(blocks...))
	return ctx, nil, nil
}
```

- [ ] Replace `runToolCallsParallel` with a helper that assumes authorization already happened:

```go
func (r *Runner) finishToolCallsParallel(ctx context.Context, st *runState, selected []tool.Tool, calls []message.ToolUse) (context.Context, []PendingToolCall, error) {
	outcomes := r.executeParallel(ctx, st, selected, calls)
	if err := r.firstBatchError(ctx, outcomes); err != nil {
		r.onError(ctx, err)
		return ctx, nil, err
	}
	if err := st.applyHandoffs(selected); err != nil {
		return ctx, nil, err
	}
	st.history = append(st.history, message.ToolResults(resultBlocks(calls, outcomes)...))
	return ctx, nil, nil
}
```

- [ ] Keep `executeParallel`, `firstBatchError`, `resultBlocks`, and `authorizeBatch` otherwise unchanged.

- [ ] Run focused tests:

```bash
go test -run 'TestRunMaxConcurrencyFallsBackToSerial|TestRunParallelExecutesConcurrently|TestRunParallelBoundedConcurrency' ./runner
```

Expected: PASS.

---

## Task 3: Implement Stream Gate

**Files:**
- Modify: `runner/runner_stream.go`

- [ ] Change `streamToolCalls` so it authorizes first, then chooses serial or parallel. Replace the early `if r.maxConcurrency > 1 && len(calls) > 1` delegation with a shared decision after `authorizeBatch`.

Use this shape:

```go
func (r *Runner) streamToolCalls(ctx context.Context, st *runState, calls []message.ToolUse, yield func(model.Event, error) bool) (context.Context, bool) {
	_, toolsByName := collectTools(st.mode.Tools)
	selected, pending, err := r.authorizeBatch(ctx, st, toolsByName, calls)
	if err != nil {
		r.emitErr(ctx, yield, err)
		return ctx, false
	}
	if len(pending) > 0 {
		pc := pending[0]
		r.emitErr(ctx, yield, &ToolDeniedError{
			Tool:     pc.Tool,
			Decision: policy.Decision{Kind: policy.DecisionSuspend, Reason: pc.Reason},
		})
		return ctx, false
	}
	if r.shouldRunBatchParallel(selected) {
		return r.finishStreamToolCallsParallel(ctx, st, selected, calls, yield)
	}
	return r.streamToolCallsSerialAuthorized(ctx, st, selected, calls, yield)
}
```

- [ ] Add a small authorized-serial stream helper so Stream does not re-authorize:

```go
func (r *Runner) streamToolCallsSerialAuthorized(ctx context.Context, st *runState, selected []tool.Tool, calls []message.ToolUse, yield func(model.Event, error) bool) (context.Context, bool) {
	blocks := make([]message.Block, 0, len(calls))
	for i, call := range calls {
		if err := ctx.Err(); err != nil {
			r.emitErr(ctx, yield, err)
			return ctx, false
		}
		if !yield(model.ToolCall{Call: call}, nil) {
			return ctx, false
		}
		newCtx, out, err := r.execute(ctx, st, selected[i], call)
		if err != nil {
			r.emitErr(ctx, yield, err)
			return ctx, false
		}
		ctx = newCtx
		if !yield(model.ToolResult{CallID: call.ID, Name: call.Name, Result: out}, nil) {
			return ctx, false
		}
		blocks = append(blocks, message.NewToolResult(call.ID, call.Name, out.Content, out.IsError))
	}
	if ctx, ok := r.emitHandoffs(ctx, st, selected, yield); !ok {
		return ctx, false
	}
	st.history = append(st.history, message.ToolResults(blocks...))
	return ctx, true
}
```

- [ ] Replace `streamToolCallsParallel` with a helper that assumes authorization already happened:

```go
func (r *Runner) finishStreamToolCallsParallel(ctx context.Context, st *runState, selected []tool.Tool, calls []message.ToolUse, yield func(model.Event, error) bool) (context.Context, bool) {
	for _, call := range calls {
		if !yield(model.ToolCall{Call: call}, nil) {
			return ctx, false
		}
	}
	ch, cancel := r.dispatchTools(ctx, st, selected, calls)
	defer cancel(nil)
	outcomes, stopped := r.drainToolResults(ch, cancel, calls, yield)
	if stopped {
		return ctx, false
	}
	if err := r.firstBatchError(ctx, outcomes); err != nil {
		r.emitErr(ctx, yield, err)
		return ctx, false
	}
	return r.finalizeStreamBatch(ctx, st, selected, calls, outcomes, yield)
}
```

- [ ] Run stream-focused tests:

```bash
go test -run 'TestStreamParallelEventsAndBatch|TestStreamParallelSuspendTreatedAsDeny|TestStreamInvariants' ./runner
```

Expected: PASS.

---

## Task 4: Update Existing Tests For Explicit Parallel Safety

**Files:**
- Modify: `runner/runner_parallel_test.go`
- Modify: `runner/v03_semantics_test.go`
- Modify: `runner/model_state_test.go` only if a helper currently creates tools without safe effects for a parallel-path assertion

- [ ] Ensure every test that asserts concurrent execution or parallel error behavior uses tools whose `Info().Effects.ParallelSafe == true`.

For custom test tool structs in `runner/v03_semantics_test.go`, update `Info()` methods used in parallel equivalence tests:

```go
func (b blockingTool) Info() tool.Info {
	return tool.Info{Name: b.name, Description: b.name, InputSchema: emptyObjSchema, Effects: tool.Effects{ParallelSafe: true}}
}
```

Apply the same explicit effects to `failingTool`, `slowCtxAwareTool`, `selfCancelTool`, and `cancelThenFailTool` where those tests intend to exercise `WithMaxConcurrency(2)` or `WithMaxConcurrency(4)`.

- [ ] Keep tests that intentionally prove fallback serial with zero-value effects using zero-value effects.

- [ ] Run all runner tests:

```bash
go test ./runner
```

Expected: PASS.

---

## Task 5: Update Docs

**Files:**
- Modify: `docs/spec/loop-semantics.md`
- Modify: `docs/roadmap.md`

- [ ] In `docs/spec/loop-semantics.md` §4.2, change the parallel strategy precondition from only `maxConcurrency = k > 1 && n > 1` to also requiring every selected tool to declare `Effects.ParallelSafe == true`.

Use this wording:

```text
### 4.2 并行策略（maxConcurrency = k > 1、n > 1，且全部工具声明 ParallelSafe）

并行执行只适用于批内每个 selected tool 的 `tool.Info().Effects.ParallelSafe == true`。
若任一工具未声明 `ParallelSafe`，整个批次按 §4.1 串行策略执行；不做批内分区。
```

- [ ] In I6 / §7.1, replace the future-language about ParallelSafe with implemented-language:

```text
I6 的并行前置条件由 Runner 在 PR5 实现：只有全批工具显式声明 `ParallelSafe` 时才进入并行求值。未声明或混合批次退回串行，因此 zero-value `Effects` 保持保守。
```

- [ ] In `docs/roadmap.md`, mark PR5 delivered after implementation:

```markdown
| PR5 ✅ | Effect-aware concurrency | Make `WithMaxConcurrency` honor `Effects.ParallelSafe` by default. | v0.6.0 |
```

and replace the PR5 section with delivered wording that states scheduling reads only `ParallelSafe` and does not special-case `Destructive`.

- [ ] Run doc-insensitive full tests:

```bash
go test ./...
```

Expected: PASS.

---

## Task 6: Final Verification

**Files:**
- All modified files

- [ ] Format Go files:

```bash
gofmt -w runner/runner.go runner/runner_stream.go runner/runner_parallel_test.go runner/v03_semantics_test.go runner/model_state_test.go
```

- [ ] Verify no formatting drift:

```bash
gofmt -l .
```

Expected: no output.

- [ ] Run vet:

```bash
go vet ./...
```

Expected: no output and exit code 0.

- [ ] Run full test suite:

```bash
go test ./...
```

Expected: PASS for every package.

- [ ] Inspect changed files:

```bash
git status --short
```

- [ ] Do not commit automatically. If the user explicitly asks for a commit, use a Conventional Commit message such as:

```text
feat(runner): gate parallel tools on effects
```

## Self-Review

- Spec coverage: tasks cover the `ParallelSafe` gate, zero-value fallback, mixed-batch fallback, Stream parity, docs, and verification.
- Placeholder scan: no TBD/TODO/fill-in-later steps.
- Type consistency: the only new helper is internal to `runner`; public API stays unchanged.
