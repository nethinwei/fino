# x/agui: AG-UI Extension Completeness Design

## Summary

Build `x/agui` as an independent extension package that adapts fino to the AG-UI protocol end-to-end. The goal is not to move AG-UI concepts into the core, but to prove that fino's minimal ReAct kernel exposes enough seams for a full external protocol layer to observe, drive, serialize, suspend, resume, and enrich a run.

## Motivation

AG-UI is a user-facing agent protocol, not just a chat stream. It requires run lifecycle events, message and tool snapshots, interrupt-aware resume, shared state synchronization, reasoning visibility, multimodal inputs, capability discovery, and transport support. fino already has a strong ReAct core, but the extension surface has to be validated against a stricter external protocol.

This project is the proof point for fino's extensibility thesis:

- if `x/agui` can implement the full protocol with thin adapters, the core is sufficiently extensible;
- if `x/agui` gets stuck, the missing piece is a generic seam, not an AG-UI-specific concept.

## Goals

1. Map AG-UI `RunAgentInput` into fino `Runner` inputs and execution options.
2. Convert fino stream events into AG-UI lifecycle, text, tool, reasoning, state, activity, and special events.
3. Support AG-UI interrupt/resume semantics, including approval, cancellation, and approve-with-edits where possible.
4. Provide an HTTP SSE transport that speaks AG-UI-compatible event streams.
5. Expose AG-UI capability discovery from fino agent, mode, runner, policy, and tool metadata.
6. Support serialization helpers for thread/run lineage, event replay, and compaction.
7. Identify the smallest core seams required for any AG-UI capability that cannot be expressed purely in `x/agui`.

## Non-Goals

- Do not move AG-UI types into the core packages.
- Do not add a graph engine, session store, or state database to the core.
- Do not require fino to become a native AG-UI runtime internally.
- Do not add provider-specific transport code outside the adapter layer.

## Architecture

`x/agui` is a boundary layer around the existing core. It owns the AG-UI protocol types, codecs, transport, serialization helpers, and capability discovery. The core stays responsible for ReAct execution, tool authorization, suspend/resume, and hook invocation.

The adapter layer works in both directions:

- inbound: AG-UI `RunAgentInput` -> fino `agent.Agent`, `runner.Runner`, `runner.Input`, `runner.RunOption`
- outbound: fino `Runner.Stream` / `Runner.Run` / `ResumeApproved` -> AG-UI `BaseEvent`

If a protocol feature needs a new seam, the seam must remain generic and reusable outside AG-UI.

## Capability Coverage Matrix

### Pure adapter coverage

These should be implemented entirely in `x/agui` without core changes:

- `RUN_STARTED`, `RUN_FINISHED`, `RUN_ERROR`
- `STEP_STARTED`, `STEP_FINISHED`
- `TEXT_MESSAGE_START`, `TEXT_MESSAGE_CONTENT`, `TEXT_MESSAGE_END`
- `TOOL_CALL_RESULT`
- `MESSAGES_SNAPSHOT`
- `RAW`, `CUSTOM`
- `REASONING_*` as derived/encoded stream events where the provider exposes reasoning content
- `STATE_SNAPSHOT`, `STATE_DELTA` when state is supplied externally or composed from app state
- `CAPABILITIES` discovery payloads
- SSE transport
- run/thread lineage and event serialization helpers

### Adapter coverage with conventions

These should be implemented in `x/agui` first, but may require core seam follow-up if the conventions are too lossy:

- `TOOL_CALL_START`, `TOOL_CALL_ARGS`, `TOOL_CALL_END`
- interrupt-aware `RUN_FINISHED { outcome: interrupt }`
- `RunAgentInput.resume`
- `approve-with-edits`
- frontend-defined tools
- multimodal user input mapping
- activity messages and deltas
- branchable event logs

### Likely core seams

If `x/agui` cannot correctly implement the protocol with adapters alone, these are the minimal core seams to add:

1. External/deferred tool execution, so a tool call can pause before execution and resume with an externally supplied result.
2. Resume with edited tool arguments, so approval flows can replace proposed tool input instead of only accepting or rejecting it.
3. Side-band event emission, so state/activity/custom events do not need to pollute model history.
4. Stable message identifiers or an equivalent adapter-visible correlation primitive.
5. Capability introspection for agent/mode/tool/multimodal/reasoning/state features.

## Package Layout

`x/agui/` should be split by responsibility:

- `x/agui/types`: AG-UI protocol structs and enums.
- `x/agui/codec`: fino <-> AG-UI event/message/tool mappings.
- `x/agui/runtime`: request execution and stream orchestration.
- `x/agui/server`: HTTP SSE endpoint and request/response plumbing.
- `x/agui/interrupt`: suspend/resume translation and approval helpers.
- `x/agui/state`: state snapshot/delta helpers.
- `x/agui/serialize`: event log, lineage, and compaction helpers.
- `x/agui/capabilities`: capability synthesis from fino core metadata.

## Validation Strategy

The extension is considered successful only if the following scenarios work end-to-end:

1. Text-only streaming chat.
2. Tool call / tool result lifecycle.
3. Human approval interrupt and resume.
4. Reasoning visibility or encrypted reasoning continuity.
5. State snapshot and delta propagation.
6. Frontend-defined tool execution via adapter-controlled resume.
7. Handoff/multi-agent continuity.
8. Event serialization and replay from `threadId`/`runId` lineage.
9. Capability discovery that allows a client to gate UI features correctly.

## Core Questions To Resolve During Implementation

1. Can AG-UI approve-with-edits be expressed through current resume seams, or does it require a generic edited-resume seam?
2. Can frontend tool execution be represented as a deferred tool lifecycle without new core types?
3. Can AG-UI state/activity events live entirely in the adapter layer, or do they need a runner-side side-band emitter?
4. Are stable message IDs sufficient as an adapter responsibility, or does the core need to expose them?

## Test Strategy

`x/agui` needs integration-style tests that exercise a real fino runner and verify the emitted AG-UI events, not just unit tests on mapping helpers.

Minimum test coverage:

- `RunStarted` / `RunFinished` happy path
- tool call lifecycle mapping
- interrupt resume mapping
- reasoning/state event propagation
- serialization round trip
- capabilities snapshot generation
- SSE transport framing
