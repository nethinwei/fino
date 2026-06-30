# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- **Breaking: `runner.NewRun` rejects a dangling tool_use tail when the resume
  seam is off.** A history whose last message is an assistant tool_use with no
  following tool_result is not a safe boundary (loop-semantics I10): most
  providers (notably Anthropic) return 400 on it. `prepareRun` now rejects it up
  front with `ErrPendingToolUseInHistory` instead of forwarding the ill-formed
  request. Enable `WithResumeFromPendingTools` to execute the pending batch
  before the first model turn, or capture the snapshot at a completed turn
  boundary. `NewResumeRun` is unaffected (its suspended snapshot legitimately
  ends in a dangling tool_use).

## [0.9.1] - 2026-06-30

### Changed

- **Breaking: the multi-turn ReAct loop left the core.** The core `runner`
  package no longer ships a multi-turn loop. `runner.Runner.Run`, `Stream`,
  `ResumeApproved`, and `WithMaxTurns` are removed. The loop now lives in
  `x/react` as `Loop.Run` / `Loop.Stream` / `Loop.ResumeApproved` with
  `react.WithMaxTurns`, reusing all `runner.*` data types. This aligns fino's
  boundary with the Anthropic SDK, where the SDK exposes messages + `tool_use`
  and the agent loop is user-written. The core keeps the single-step primitives
  and the execution-consistency mechanisms (suspend/resume, three-state policy,
  effect-aware concurrency, idempotency, stream-contract checks) that cannot be
  reconstructed by wrapping `Tool`, `Policy`, `Hook`, `Mode`, or `Model`.

### Added

- **Core single-step ReAct primitives.** `runner.Runner.NewRun` creates a
  per-run session; `Run.Step` and `Run.StreamStep` drive one ReAct turn (model
  call + authorize/execute the tool batch); `Run.ResumePendingIfEnabled` runs
  the resume-from-pending tail batch; `Runner.NewResumeRun` rebuilds a run from
  a `SuspendedRun` and executes the approved batch without re-consulting the
  policy, leaving the post-resume turns to the caller; `Runner.FireOnError`
  fires the `OnError` hook for loop-level errors (e.g. max turns) observed by
  the outer loop; `Run.Result` / `Run.SuspendedResult` build the terminal
  `*Result`.
- **`x/react` reference loop.** `react.New(*runner.Runner, opts...)` returns a
  `*Loop` with `WithMaxTurns`; `Loop.Run` / `Stream` / `ResumeApproved` mirror
  the shapes the core used to provide, composed from the single-step primitives.

### Migration

Replace `r.Run(...)` / `r.Stream(...)` / `r.ResumeApproved(...)` /
`runner.WithMaxTurns` with a `react.Loop`: build `r, _ := runner.New(m,
opts...)`, then `l, _ := react.New(r, react.WithMaxTurns(n))`, then call
`l.Run` / `l.Stream` / `l.ResumeApproved`. `runner` data types (`Result`,
`Input`, `SuspendedRun`, `Approval`, `PendingToolCall`, `RunOption`,
`WithMode`, `WithRunID`, `WithModelOptions`, `WithResumeFromPendingTools`,
`SuspendedRunFrom`, errors) are unchanged. To write your own loop, drive
`rn, _ := r.NewRun(ctx, a, input, opts...)` and call `rn.Step()` (or
`rn.StreamStep(yield)`) until `StepCompleted` / `StepSuspended`.

## [0.9.0] - 2026-06-04

### Added

- **`x/agui` — AG-UI protocol adapter** — `x/agui` is a new extension package
  that bridges fino's ReAct runner to the
  [AG-UI protocol](https://docs.ag-ui.com/concepts/events). A `Runtime` wraps a
  `Runner` and `Agent` and exposes a single `Stream(ctx, RunAgentInput)` method
  that emits typed AG-UI `Event` values over `iter.Seq2[Event, error]`.
  - **Lifecycle events** — `RUN_STARTED`, `RUN_FINISHED`, `RUN_ERROR`,
    `STEP_STARTED`, `STEP_FINISHED` map to runner turns and termination.
  - **Text streaming** — `TEXT_MESSAGE_START/CONTENT/END` stream assistant text
    deltas in real time.
  - **Tool events** — `TOOL_CALL_START/ARGS/END` and `TOOL_CALL_RESULT` cover
    the full tool-execution lifecycle including streaming argument chunks.
  - **Messages snapshot** — `MESSAGES_SNAPSHOT` emitted on every turn so
    clients can reconstruct conversation history without replaying the event log.
  - **Reasoning events** — `REASONING_START/END` envelope and per-block
    `REASONING_MESSAGE_START/CONTENT/END` events map fino's `TypeThinking` blocks
    to the AG-UI reasoning sub-protocol; multiple thinking blocks in a single
    turn each get a distinct `messageId`.
  - **State and activity events** — `STATE_SNAPSHOT/DELTA` and
    `ACTIVITY_SNAPSHOT/DELTA` (RFC 6902 JSON Patch) side-band emitters in
    `sideband.go` let the host push arbitrary state and activity updates without
    entering fino's model history.
  - **Raw and custom events** — `NewRawEvent` and `NewCustomEvent` for
    pass-through and extensibility.
  - **Interrupt-aware `RUN_FINISHED`** — when the runner suspends mid-run,
    `Stream` persists the snapshot to a `SuspendStore`, emits
    `RUN_FINISHED{interrupt: {pendingCalls: [...]}}`, and accepts a subsequent
    resume via `RunAgentInput.Resume`. Resume is fail-closed: without a
    `SuspendStore` or a matching snapshot the run errors rather than executing
    client-supplied history.
  - **Serialization** — `MarshalEventLog` / `UnmarshalEventLog` round-trip the
    full event log with discriminated-type restoration; unknown types come back
    as `RawEvent` for forward compatibility. `Compact` reduces a log to its
    latest `MESSAGES_SNAPSHOT` plus trailing events, returning a fresh copy.
  - **`Lineage`** — `{threadId, runId, parentRunId}` struct for adapter-owned
    correlation IDs that survive serialization.
  - **`Runtime.Capabilities()`** — returns `Capabilities{HasSuspendResume,
    Tools}` reflecting the actual runtime configuration.

## [0.8.0] - 2026-06-03

### Added

- **Stream-native suspension** — on the `Runner.Stream` path a Policy
  `DecisionSuspend` now emits a terminal `model.Suspended` event carrying a
  neutral snapshot (messages, agent/mode name, run ID, pending calls) instead
  of being downgraded to a `ToolDeniedError`. Rebuild a `runner.SuspendedRun`
  with the new `runner.SuspendedRunFrom` and resume via `ResumeApproved`, so
  `Stream` has the same suspend/resume semantics as `Run`. `model.Suspended` is
  a new sealed `model.Event`; a provider must not yield it (treated as
  `ErrStreamContract`, like a provider-yielded `FinalMessage`).
- **Tool-use ID invariants** — the Runner rejects empty or duplicate `tool_use`
  IDs before authorizing or executing any tool (`ErrInvalidToolCallID` /
  `ErrDuplicateToolCallID`), enforced in both the `Run`/`Stream` batch path and
  in `ResumeApproved`'s dangling batch (loop-semantics I14).
- **Approval-snapshot consistency** — `ResumeApproved` verifies each pending
  call's name and byte-level input against the dangling `tool_use` it will
  execute, rejecting a mismatched `SuspendedRun` before any tool runs.

### Changed

- **Behavior change:** `Runner.Stream` no longer downgrades a suspended batch to
  `ToolDeniedError`. Consumers that relied on that deny error must handle the
  terminal `model.Suspended` event instead.

## [0.7.0] - 2026-06-03

### Added

- **Coding agent reference proof** — `examples/coding_agent/` demonstrates all
  fino core mechanisms end-to-end: ReAct loop, dual-mode agent (plan/code),
  effect-aware policy (suspend on `RequiresApproval`), human-in-the-loop
  approve/reject, streaming output (plan phase via `Runner.Stream` with live
  `TextDelta`), and deterministic replay testing. The example is a reference
  proof, not a product; it lives entirely in `examples/` with no new core or
  `x/` packages.
  - `tools.go` — four tool constructors (`read_file`, `list_files`,
    `write_file`, `run_tests`) with declared `Effects`, an `approvalPolicy`,
    and a `buildAgent` helper.
  - `main.go` (`//go:build !record`) — CLI with plan→code loop, recording via
    `RecordingModel/Tool/Policy`, 5-minute context timeout, trace hooks.
  - `record_fixture.go` (`//go:build record`) — auto-approve recording script
    for rebuilding fixtures.
  - `replay_test.go` — direct Runner replay with `ReplayModel/Tool/Policy`,
    asserts suspend count matches tape.
  - `testdata/` — pre-recorded fixture with parallel `read_file` + `write_file`
    suspend/approve/resume cycle.

## [0.6.0] - 2026-06-02

### Added

- **Idempotency boundary** — the Runner now injects a run-scoped
  `tool.ExecutionContext{RunID, ToolCallID, IdempotencyKey}` into the context
  passed to every tool's `Run` (and to the `BeforeTool` hook), read via
  `tool.ExecutionContextFrom`. `runner.WithRunID` supplies the run ID; the
  `IdempotencyKey` is a deterministic function of `(RunID, ToolCallID)` — empty
  when no `RunID` is set, so behavior is fully backward compatible. The context
  is an audit and deduplication hint, not a security boundary, and the Runner
  adds no automatic retries.
- **`SuspendedRun.RunID`** — suspended runs now carry the run ID so
  `ResumeApproved` restores it (overriding any `WithRunID` passed on resume),
  giving approved and previously-allowed calls the same `IdempotencyKey` they
  would have had in the original run (loop-semantics I13). Fixtures without the
  field deserialize to `""`.
- **`x/replay.ToolRecord.CallID`** — `RecordingTool` records the tool_use ID
  from the execution context, so the execution tape gains per-call correlation
  (audit and idempotency share one identifier). Legacy fixtures without `callID`
  still load.

### Changed

- **Effect-aware concurrency** — `runner.WithMaxConcurrency(n > 1)` now treats
  concurrency as an upper bound gated by `tool.Effects.ParallelSafe`: a tool-call
  batch only runs concurrently when every selected tool explicitly declares
  `ParallelSafe`; otherwise the whole batch falls back to serial execution.
  Zero-value `Effects` remains conservative, and the Runner does not infer
  safety from `ReadOnly`, `Idempotent`, `ExternalWrite`, or `Destructive`.
- **Serial `Stream` now authorizes the whole batch before executing** — the
  serial streaming path previously authorized each tool just before running it,
  so an earlier tool could execute and emit `ToolCall`/`ToolResult` events before
  a later call in the same batch was denied or suspended. It now authorizes the
  entire batch first (matching `Run` and loop-semantics §4.1/I6): if any call is
  denied or suspended, no tool in the batch executes and no `ToolCall` event is
  emitted.

## [0.5.0] - 2026-06-02

### Added

- **Execution tape** — `x/replay` now records a structured `Log.Events` tape over
  public seams in addition to the existing `Log.Model` / `Log.Tools` replay
  source: model responses, policy decisions, tool executions, suspensions,
  approvals, resumes, and termination. `Event` uses a string `Kind` plus a
  kind-specific payload so JSON fixtures stay readable. `Log.Marshal` /
  `Unmarshal` include `events`; legacy fixtures without that field still load
  (`Events` is nil) and replay still drives from `Model` / `Tools`. The tape is
  reproducibility and audit evidence, not proof of business correctness: no
  exactly-once side effects, durable workflow, or tamper resistance.
- **RecordingPolicy / ReplayPolicy** — `RecordingPolicy` wraps a `policy.Policy`
  and records each decision (including policy-system errors) without changing
  behavior. `ReplayPolicy` replays recorded decisions in order so a replayed run
  never re-consults human approval, clocks, RBAC, allowlists, or risk scoring; it
  validates the current request against the recorded one on stable identity
  (`AgentName`, `ModeName`, `Tool.Name`, raw `Input`), returning a `replay:` error
  on a fixture mismatch (distinct from a policy deny).
- **Boundary recorders** — `RecordSuspend`, `RecordApproval`, `RecordResume`, and
  `RecordTermination` record public run boundaries that no model/tool/policy
  wrapper can observe. They copy the top-level caller-owned slices so later
  replacement of a caller-held element cannot rewrite the tape (nested user-owned
  data such as `message.Block` content or `tool.Info.Metadata` is not deep-copied),
  and never change runner control flow.
- **eval.RunWithOptions** — `x/eval` gains `RunWithOptions(ctx, c, opts...)` so a
  policy-sensitive case can wire `runner.WithPolicy(&replay.ReplayPolicy{Log: c.Log})`
  without changing the `Case` shape. `Run` delegates to it with no extra options.

## [0.4.0] - 2026-06-02

### Added

- **Approved resume** — `runner.ResumeApproved` continues a suspended run after a
  human approves or rejects its pending tool calls. `Result.SuspendedRun()`
  extracts a `SuspendedRun` snapshot (carrying `LastAgentName` and
  `LastMode` — only the agent *name*, no live object reference; the live agent is
  passed back by the caller). The snapshot is plain data; whether it JSON-marshals
  depends on each `PendingCall`'s `tool.Info.Metadata` being marshalable (the core
  does not sanitize it). Callers collect
  `Approval` values (approve/reject per CallID) and pass them to `ResumeApproved`,
  which validates them, executes approved (and previously allowed) calls, writes
  rejections as model-visible `tool_result` blocks, and resumes the ReAct loop.
  It does not re-consult the policy — human approval is the final decision.
  ResumeApproved also rejects a snapshot that leaks a system message
  (`ErrSystemMessageInHistory`, protecting the no-system-leak invariant), has no
  pending calls, or tries to override the mode.
- **ApprovalError / ErrInvalidApproval** — typed validation error (missing,
  unknown, or duplicate approvals; and malformed-snapshot cases) wrapping the
  `ErrInvalidApproval` sentinel, kept distinct from `ErrNotSuspended` (returned by
  `Result.SuspendedRun()` on a completed result) and `ErrResumeAgentMismatch`
  (wrong agent on resume).

### Changed

- **Loop semantics** — `docs/spec/loop-semantics.md` gains the `[T-RESUME]`
  transition and invariant **I12** (approval-resume completeness), and §7.2 is
  split into safe-boundary recovery (I10) and first-class approval resume (§7.3,
  I12). The `hitl_resume` cookbook now uses `DecisionSuspend` + `SuspendedRun` +
  `ResumeApproved` instead of the blind `WithResumeFromPendingTools` seam.

## [0.3.0] - 2026-06-02

### Added

- **Typed tool effects** — `tool.Effects` and the `tool.WithEffects` option let
  tool authors declare a tool's effect profile (read-only, idempotent,
  parallel-safe, destructive, external-write, requires-approval, sensitive
  input/output) at registration. Effects are surfaced to policies through the
  existing `policy.Request.Tool` field. Declaration-only: the Runner does not
  yet change behavior based on Effects (a foundation for effect-aware
  concurrency and approval in later releases).
- **Three-state policy (allow / deny / suspend)** — `policy.DecisionKind` adds a
  `DecisionSuspend` state alongside allow and deny, with a safe
  `DecisionUnspecified` zero value and `Decision.ResolvedKind()` migration rule.
  When a policy suspends a tool call, `runner.Run` halts with a
  `Result{Suspended: true, PendingCalls: [...]}` (see `runner.PendingToolCall`)
  instead of erroring — `OnError` does not fire. This is the seam for
  human-in-the-loop approval.

### Changed

- **Authorize-all-before-execute** — the serial tool path now authorizes the
  whole batch before executing any tool, matching the parallel path. A deny or
  suspend anywhere in a batch now produces no side effects from earlier calls
  (strengthens serial/parallel protocol-trace equivalence, I6).
- **Streaming suspend** — `Runner.Stream` downgrades a suspend decision to a
  `ToolDeniedError` rather than introducing a new event type; full suspend
  semantics are available on `Runner.Run`.
- **Loop semantics** — `docs/spec/loop-semantics.md` gains the `[T-SUSPEND]`
  transition and invariant I11 (suspend precision), and updates I3/I4/I5.

### Compatibility

- Fully backward compatible. Existing `policy.Policy` implementations returning
  `Decision{Allow: true/false}` keep working via `ResolvedKind()`; `AllowAll`
  now returns `Decision{Kind: DecisionAllow}`. Adding `tool.Effects` to
  `tool.Info` is additive; tools built without `WithEffects` get the
  conservative zero value.

## [0.2.1] - 2026-06-02

### Changed

- Tightened reliability claims around replay, recovery, and parallel execution:
  current guarantees are scoped to recorded model/tool effects, safe-boundary
  continuation, and protocol-trace equivalence under tool-independence
  assumptions. Full effect-aware approval/resume and execution-tape semantics
  are tracked in the roadmap rather than claimed as shipped behavior.
- Added `docs/roadmap.md` describing the path toward typed tool effects,
  suspend/resume, execution tapes, and effect-aware concurrency.

## [0.2.0] - 2026-06-02

### Added

- **Formal loop semantics** — `docs/spec/loop-semantics.md` specifies the ReAct
  loop as a state-transition system with ten invariants (I1–I10).
- **Property-based invariant tests** — `runner/invariants_test.go` verifies
  nine of the ten invariants (I1–I9) over many random scripts at both serial and
  parallel concurrency, including protocol-trace equivalence under
  tool-independence assumptions; the tenth (I10, safe-boundary continuation) is
  covered separately by a seam probe
  (`runner/recover_seam_test.go`).
- **`x/` reference compositions** — `x/replay`, `x/recover`, `x/trace`,
  `x/budget`, and `x/eval`: constructive evidence that the core's seams suffice,
  each standard-library only and never imported by the core.
- **Design** — sufficiency thesis and seam discipline added to `docs/design.md`.

### Changed

- **finocode extracted** — the flagship coding-agent reference app moved out of
  `examples/` into its own repository
  ([nethinwei/finocode](https://github.com/nethinwei/finocode)) so it can grow
  its own dependencies without affecting fino's standard-library-only core.

## [0.1.0] - 2026-06-02

First tagged release. A minimal, reliable ReAct agent SDK for Go, built from
small composable primitives; the core depends on the standard library only.

### Added

- **Core ReAct loop** — `runner.Run` and `runner.Stream` with turn limits,
  pre-execution policy authorization, lifecycle hooks, clean termination, and
  opt-in bounded parallel tool execution (`runner.WithMaxConcurrency`).
- **Streaming as semantic events** — text and reasoning deltas, tool calls,
  tool results, handoffs, and a final-message snapshot over `iter.Seq2`, with a
  single consistent error path (`model.StreamError` + iterator error).
- **Agent & Mode** — one agent with multiple personas (instructions, tools,
  model options), plus model-driven handoffs modeled as ordinary tools.
- **Extension points** — `model.Model`, `tool.Tool` (+ `tool.NewFunc` with
  JSON Schema inference), `policy.Policy`, and `hooks.Hooks`.
- **Provider adapters** — `openai` and `anthropic` generic adapters plus
  `deepseek`, `kimi`, `glm`, `qwen`, and `minimax` presets, with streaming-safe
  connection timeouts and retry-with-backoff (`WithTimeout`, `WithMaxRetries`).
- **Examples** — `hello`, `multi_mode`, `streaming`, `history_trim`, and
  `finocode` (an interactive coding agent).
- **Docs** — bilingual README (English / 简体中文) and `docs/design.md`.

[Unreleased]: https://github.com/nethinwei/fino/compare/v0.9.1...HEAD
[0.9.1]: https://github.com/nethinwei/fino/compare/v0.9.0...v0.9.1
[0.9.0]: https://github.com/nethinwei/fino/compare/v0.8.0...v0.9.0
[0.8.0]: https://github.com/nethinwei/fino/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/nethinwei/fino/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/nethinwei/fino/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/nethinwei/fino/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/nethinwei/fino/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/nethinwei/fino/compare/v0.2.1...v0.3.0
[0.2.1]: https://github.com/nethinwei/fino/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/nethinwei/fino/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/nethinwei/fino/releases/tag/v0.1.0
