# fino Roadmap: Semantically Sufficient Runtime Kernel

This roadmap keeps fino's product boundary narrow: a small Go runtime kernel for
tool-using agents, not an application-owning framework.

## Target Positioning

fino should be described as:

> A minimal, specified runtime kernel for tool-using agents: effect-aware,
> suspendable, resumable, replayable, and testable through explicit execution
> contracts.

The core continues to provide the ReAct loop and the seams required to execute it
correctly. Product features such as graph orchestration, persistence, RAG, MCP,
UI, enterprise RBAC, sandbox implementations, and prompt-injection classifiers
remain outside the core.

## Core Rule

Concrete business policy stays outside the core. Mechanisms that determine
execution consistency, safe recovery, auditability, and replay equivalence belong
in the core when they cannot be reconstructed reliably by wrapping `Tool`,
`Policy`, `Hook`, `Mode`, `Model`, or `Runner` calls.

## What Stays Out Of Core

| Capability | Home |
| --- | --- |
| Graph / DAG orchestration | Application code or workflow libraries |
| RAG loaders, chunking, embeddings, retrieval | Application tools or examples |
| MCP protocol implementation | `x/mcp` or application adapters |
| Session stores, SQLite, Redis, cloud persistence | `x/store` or application code |
| CLI / Web UI | Reference apps |
| Enterprise RBAC rules | User `policy.Policy` implementations |
| Sandbox implementation | `x/sandbox` or application code |
| Prompt-injection classifier models | `x/security` or application code |

## What Becomes Core Semantics

| Mechanism | Reason |
| --- | --- |
| Typed `tool.Effects` | Runner, Policy, replay, retry, audit, and concurrency need a shared contract. |
| Suspend / resume | Approval must execute the original pending call, not ask the model to regenerate it. |
| Execution tape contract | Replay needs a defined boundary for all behavior-affecting execution effects. |
| Effect-aware concurrency | Ordered transcripts are not enough to guarantee external-state safety. |
| Idempotency / retry boundary | Recovery and retries must not silently duplicate irreversible side effects. |

## PR Sequence

| PR | Theme | Output | Version Target |
| --- | --- | --- | --- |
| PR0 | Contract scope | Tighten README/design/spec/changelog claims around replay, recovery, and parallelism. | v0.2.1 |
| PR1 | Tool effects | Add `tool.Effects`, `tool.WithEffects`, and surface effects through `policy.Request.Tool`. | v0.3.0 |
| PR2 | Three-state policy | Add Allow / Deny / Suspend decisions and suspended `runner.Result`. | v0.3.0 |
| PR3 | Approved resume | Add `SuspendedRun`, `PendingToolCall`, `Approval`, and `Runner.ResumeApproved`. | v0.4.0 |
| PR4 ✅ | Execution tape | Define recorded model, policy, tool, suspension, resume, and termination events. | v0.5.0 |
| PR5 ✅ | Effect-aware concurrency | Make `WithMaxConcurrency` honor `Effects.ParallelSafe` by default. | v0.6.0 |
| PR6 ✅ | Idempotency boundary | Define idempotency keys and retry constraints without automatic write retries. | v0.6.0 |
| PR7 | Reference proof | Build a small safe coding-agent flow proving approval, resume, replay, and safe parallelism. | v0.7.0 |

## PR0: Contract Scope

PR0 is docs-only. It protects future API design by removing claims the current
runtime cannot yet prove.

Required changes:

| Current claim | Replacement |
| --- | --- |
| Reliable complex agent capability needs no framework. | Reliable execution infrastructure for complex tool-using agents does not require an application-owning framework; it requires a semantically sufficient runtime kernel, explicit effect boundaries, and composable policies. |
| `resume-completeness` | `safe-boundary continuation completeness` until a first-class approval/resume API exists. |
| Parallel / serial equivalence | Protocol-trace equivalence under tool-independence assumptions. |
| `model.Model` + `tool.Tool` are the only effect inputs. | Model and tool effects are currently recorded; policy decisions and behavior-affecting interceptors must be recorded before replay can claim full execution equivalence. |

PR0 should not add new APIs.

## PR1: Typed Tool Effects

Add the smallest effect vocabulary needed by execution machinery:

```go
type Effects struct {
    ReadOnly         bool
    Idempotent       bool
    ParallelSafe     bool
    Destructive      bool
    ExternalWrite    bool
    RequiresApproval bool
    SensitiveInput   bool
    SensitiveOutput  bool
}
```

Zero value means unspecified and conservative. The Runner must not infer that a
tool is safe just because a field is false.

## PR2: Three-State Policy

Upgrade policy decisions from allow/deny to allow/deny/suspend. To avoid unsafe
zero-value behavior, the decision kind should include an unspecified state:

```go
type DecisionKind uint8

const (
    DecisionUnspecified DecisionKind = iota
    DecisionAllow
    DecisionDeny
    DecisionSuspend
)
```

Migration rule: if `Kind == DecisionUnspecified`, preserve current behavior by
interpreting `Allow == true` as allow and `Allow == false` as deny.

First implementation rule: if any call in a tool batch suspends, the whole batch
suspends and no tool in that batch executes.

## PR3: Approved Resume

Add a first-class API that executes approved original calls, not regenerated
model calls:

```go
type PendingToolCall struct {
    AgentName string
    ModeName  string
    Tool      tool.Info
    Call      message.ToolUse
    Reason    string
    InputHash string
}

type Approval struct {
    CallID   string
    Approved bool
    Reason   string
}
```

`Runner.ResumeApproved` must validate that approvals match pending calls and that
the approved input hash matches the original pending input. Rejections should be
written as tool results visible to the model rather than hidden control flow.

This does not claim exactly-once external side effects across process crashes.
That requires durable execution storage and idempotent tools.

## PR4: Execution Tape

Define replay as a recorded execution trace, not a proof of business correctness.

The tape should record model outputs, policy decisions, tool executions,
suspensions, approvals, resumes, and termination. Replay should not call real
providers or real side-effecting tools.

Delivered in `x/replay` as an `Events` tape layered over the existing
`Log.Model`/`Log.Tools` replay source: `RecordingPolicy`/`ReplayPolicy` cover the
policy seam, and `RecordSuspend`/`RecordApproval`/`RecordResume`/`RecordTermination`
record run boundaries explicitly. `x/eval` adds `RunWithOptions` so policy-sensitive
fixtures can wire `ReplayPolicy`. No core package changed.

## PR5: Effect-Aware Concurrency

Delivered as conservative scheduling driven only by the explicit
`ParallelSafe` declaration:

| Tool batch | Scheduling |
| --- | --- |
| All tools declare `ParallelSafe` | Execute concurrently, preserve result order. |
| Any tool lacks `ParallelSafe` | Execute the whole batch serially. |
| Any tool suspends | Execute none; return suspended. |
| Destructive tools | Prefer serial by not declaring `ParallelSafe`; Runner does not special-case `Destructive`. |

No batch partitioning in the first version. `WithMaxConcurrency` is an upper
bound, not a force-parallel switch: the Runner only enters the parallel path when
the whole selected batch explicitly opts into `ParallelSafe`.

## PR6: Idempotency And Retry Boundary

✅ Completed. Delivered the minimal contract without automatic Runner retries:

```go
type ExecutionContext struct {
    RunID          string
    ToolCallID     string
    IdempotencyKey string
}
```

The Runner injects this context before every tool's `Run` (and before the
`BeforeTool` hook) via `tool.ContextWithExecutionContext`; tools read it with
`tool.ExecutionContextFrom`. `runner.WithRunID` supplies the run ID;
`SuspendedRun.RunID` restores it on `ResumeApproved` so resumed calls keep the
same `IdempotencyKey` (loop-semantics I13). `x/replay.ToolRecord.CallID` records
the per-call identifier on the tape. See
`docs/superpowers/specs/2026-06-02-idempotency-boundary-design.md`.

Read-only tools can be retried by add-ons. Idempotent writes require the same
idempotency key. Non-idempotent destructive writes must not be retried
automatically. No automatic retries are added in core.

## PR7: Reference Proof

Build a small safe coding-agent flow, not a full Claude Code clone:

1. Plan mode uses read-only tools.
2. Code mode proposes a `write_file` diff.
3. `write_file` requires approval and suspends.
4. The CLI displays the exact diff.
5. Approval resumes the exact original call.
6. A test tool runs after the write.
7. An execution tape replays the completed run without calling the model or
   writing files again.

## Version Plan

| Version | Merge condition |
| --- | --- |
| v0.2.1 | PR0 merged. |
| v0.3.0 | PR1 and PR2 merged. |
| v0.4.0 | PR3 merged with approval example. |
| v0.5.0 | PR4 merged with replay/eval tests. |
| v0.6.0 | PR5 and PR6 merged. |
| v0.7.0 | PR7 reference proof merged. |
| v1.0.0 | Contracts, property tests, replay fixtures, and reference case study are stable. |

## Non-Claims

Even after this roadmap, fino should not claim:

- It solves prompt injection.
- It guarantees agent correctness.
- It provides exactly-once external side effects.
- It makes all parallel tool execution equivalent.
- It solves durable workflows without persistence requirements.
