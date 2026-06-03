# PR9: AG-UI Extension Layer Implementation Plan

**Goal:** Build `x/agui` as a full AG-UI compatibility adapter that proves fino
can support a user-facing agent protocol without moving AG-UI concepts into the
core runtime.

**Architecture:** `x/agui` owns protocol types, event mapping, runtime
orchestration, SSE transport, state helpers, serialization, and capability
discovery. Core packages remain unchanged unless an end-to-end test proves that
a missing capability cannot be expressed through existing public seams.

**Protocol references:**

- <https://docs.ag-ui.com/concepts/events>
- <https://docs.ag-ui.com/concepts/architecture>
- <https://github.com/ag-ui-protocol/ag-ui>

## Assumptions

- AG-UI lifecycle, text, tool, state, message snapshot, activity, special, and
  reasoning events are stable adapter targets.
- Interrupt-aware `RunFinished.outcome`, `RunAgentInput.resume`, and lineage
  fields are draft protocol features. `x/agui` may support them, but core fino
  contracts must not depend on their current shape.
- `x/agui` may depend only on the Go standard library and fino public packages
  unless a concrete protocol requirement proves that a dependency is necessary.
- Message IDs are adapter-owned until an integration test proves that a stable
  core correlation primitive is required.
- State, activity, raw, and custom events remain adapter-owned side-band data
  unless an integration test proves that Runner must emit generic side-band
  events.

## Package Boundary

Start with a single `x/agui` package. Split into subpackages only when the
implementation exceeds the repository's file-size limits or a dependency
boundary becomes real. Do not create empty package scaffolding.

Initial files:

```text
x/agui/types.go
x/agui/types_test.go
x/agui/codec.go
x/agui/codec_test.go
x/agui/runtime.go
x/agui/runtime_test.go
x/agui/sse.go
x/agui/sse_test.go
```

Later files are added only when required by tested behavior:

```text
x/agui/state.go
x/agui/serialize.go
x/agui/capabilities.go
```

## Phase 1: Protocol Types And Pure Event Mapping

Define the smallest JSON-serializable AG-UI type set needed by fino stream
events:

- `RunAgentInput`
- lifecycle events: `RUN_STARTED`, `RUN_FINISHED`, `RUN_ERROR`
- text events: `TEXT_MESSAGE_START`, `TEXT_MESSAGE_CONTENT`,
  `TEXT_MESSAGE_END`
- tool events: `TOOL_CALL_START`, `TOOL_CALL_ARGS`, `TOOL_CALL_END`,
  `TOOL_CALL_RESULT`
- `MESSAGES_SNAPSHOT`
- `RAW`, `CUSTOM`

Map fino events without executing a Runner:

| fino event | AG-UI output |
| --- | --- |
| `model.TextDelta` | `TEXT_MESSAGE_START` once before the first delta, then `TEXT_MESSAGE_CONTENT` per delta |
| `model.TurnMessage` | text end and tool call start/args/end as needed |
| `model.ToolResult` | `TOOL_CALL_RESULT` |
| `model.FinalMessage` | `RUN_FINISHED` |
| `model.StreamError` or iterator error | `RUN_ERROR` |
| `model.Suspended` | draft interrupt-aware `RUN_FINISHED` |

Success criteria:

1. JSON round-trip tests pin event type names and required fields.
2. Text deltas produce exactly one start, ordered content events, and one end.
3. Tool calls use the fino `tool_use` ID as `toolCallId`.
4. Adapter-generated message IDs are stable within one run mapping.
5. No core package changes.

## Phase 2: Runtime Orchestration And Suspension

Add an adapter runtime that accepts `RunAgentInput`, builds explicit
`runner.Input` and `runner.RunOption` values, consumes `Runner.Stream`, and emits
AG-UI events.

Success criteria:

1. A real fino Runner produces `RUN_STARTED` followed by `RUN_FINISHED`.
2. Tool call and tool result lifecycles are emitted end-to-end.
3. A Policy suspension emits an interrupt-aware terminal event containing the
   original pending call.
4. Resume approval rebuilds `runner.SuspendedRun` and executes the exact
   original call.
5. Cancellation produces `RUN_ERROR` or the documented protocol cancellation
   outcome without executing pending tools.

Core seam decisions:

- If approve-with-edits or frontend-defined tools cannot be implemented without
  executing a different call than the approved call, stop and design a generic
  edited/deferred execution seam before modifying core.
- If state/activity side-band events cannot live entirely in the adapter layer
  without polluting fino model history, stop and design a generic side-band
  emission seam before modifying core.
- If adapter-owned message IDs are insufficient for protocol correlation (e.g.,
  a client requires stable IDs that survive resume or replay), stop and evaluate
  whether a core identifier primitive is warranted before modifying core.

## Phase 3: SSE Transport

Add an HTTP handler that decodes `RunAgentInput` and streams AG-UI events as
Server-Sent Events using only `net/http`.

Success criteria:

1. Correct `Content-Type: text/event-stream`.
2. Each event is JSON encoded in an SSE `data:` frame.
3. Frames flush as events arrive.
4. Client cancellation stops the fino run.
5. Encoding or runtime errors terminate with a protocol error event when
   possible.

## Phase 4: State, Activity, Reasoning, And Special Events

Add adapter-owned emitters and helpers for:

- `STATE_SNAPSHOT`, `STATE_DELTA`
- `ACTIVITY_SNAPSHOT`, `ACTIVITY_DELTA`
- `REASONING_*`
- `RAW`, `CUSTOM`

Success criteria:

1. State and activity deltas use RFC 6902 JSON Patch shapes.
2. Side-band events do not enter fino model history.
3. Reasoning mapping only exposes content already surfaced by the provider or
   fino message blocks; it does not invent or reveal hidden chain-of-thought.
4. No core side-band emitter is added unless an integration test demonstrates a
   protocol capability that the adapter cannot express.

## Phase 5: Serialization, Lineage, And Capabilities

Add helpers for event log serialization, thread/run lineage, compaction, and
capability discovery.

Success criteria:

1. Event logs round-trip without losing discriminated event types.
2. `threadId`, `runId`, and optional `parentRunId` remain adapter-owned.
3. Capability discovery reports only behavior that the configured adapter can
   actually execute.
4. Branching and compaction helpers do not introduce a session store.

## Phase 6: Completeness Audit

Run the design validation scenarios end-to-end:

1. Text-only streaming chat.
2. Tool call / tool result lifecycle.
3. Human approval interrupt and resume.
4. Reasoning visibility or encrypted reasoning continuity.
5. State snapshot and delta propagation.
6. Frontend-defined tool execution via adapter-controlled resume.
7. Handoff/multi-agent continuity.
8. Event serialization and replay from `threadId` / `runId` lineage.
9. Capability discovery.

For each failure, classify it as:

- adapter implementation gap;
- protocol draft ambiguity;
- missing generic core seam.

Only the third category may justify a core change.

## Verification

Before each PR:

```bash
gofmt -l .
go vet ./...
go test ./...
```

Each PR must focus on one phase or one proven core seam. Do not combine protocol
types, transport, state machinery, and core API changes in one review.
