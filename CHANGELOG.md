# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[0.4.0]: https://github.com/nethinwei/fino/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/nethinwei/fino/compare/v0.2.1...v0.3.0
[0.2.1]: https://github.com/nethinwei/fino/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/nethinwei/fino/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/nethinwei/fino/releases/tag/v0.1.0
