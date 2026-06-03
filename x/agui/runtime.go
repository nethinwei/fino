package agui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/runner"
)

// Errors returned by Runtime construction, message conversion, and resume.
var (
	ErrMissingRunner     = errors.New("runner is required")
	ErrMissingAgent      = errors.New("agent is required")
	ErrConvertMessage    = errors.New("cannot convert AG-UI message")
	ErrResumeUnavailable = errors.New("resume requires a SuspendStore")
	ErrNoSuspendedRun    = errors.New("no suspended run for thread")
)

// Runtime bridges a RunAgentInput to fino runner.Stream and maps the resulting
// fino events into AG-UI events using a Mapper.
type Runtime struct {
	r     *runner.Runner
	a     *agent.Agent
	store SuspendStore
}

// RuntimeOption configures a Runtime.
type RuntimeOption func(*Runtime)

// WithSuspendStore enables suspend/resume by giving the Runtime a place to
// persist the snapshot a run produces when a Policy suspends it, and to restore
// that exact snapshot on resume. Without a store, resume requests fail closed:
// the Runtime never rebuilds a suspended run from client-supplied messages, so a
// caller cannot forge a history to execute a tool the Policy never authorized.
func WithSuspendStore(store SuspendStore) RuntimeOption {
	return func(rt *Runtime) { rt.store = store }
}

// NewRuntime creates a Runtime from a configured Runner and Agent.
func NewRuntime(r *runner.Runner, a *agent.Agent, opts ...RuntimeOption) (*Runtime, error) {
	if r == nil {
		return nil, ErrMissingRunner
	}
	if a == nil {
		return nil, ErrMissingAgent
	}
	rt := &Runtime{r: r, a: a}
	for _, opt := range opts {
		if opt != nil {
			opt(rt)
		}
	}
	return rt, nil
}

// Stream returns an iterator over AG-UI events for one run. The first event is
// always RUN_STARTED; the last is RUN_FINISHED (success or interrupt) or
// RUN_ERROR. When input.Resume is non-empty the run resumes a prior suspension
// loaded from the SuspendStore; input.Messages is ignored on the resume path.
func (rt *Runtime) Stream(ctx context.Context, input RunAgentInput, opts ...runner.RunOption) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		rt.streamRun(ctx, yield, input, opts)
	}
}

func (rt *Runtime) streamRun(ctx context.Context, yield func(Event, error) bool, input RunAgentInput, opts []runner.RunOption) {
	mapper, err := NewMapper(input.ThreadID, input.RunID)
	if err != nil {
		yield(runErrEvent(input.RunID, err), err)
		return
	}
	if !yield(mapper.RunStarted(input.ParentRunID), nil) {
		return
	}
	runOpts := append([]runner.RunOption{runner.WithRunID(input.RunID)}, opts...)
	if len(input.Resume) > 0 {
		rt.streamResume(ctx, yield, mapper, input.Resume, runOpts)
		return
	}
	finoMsgs, err := convertMessages(input.Messages)
	if err != nil {
		yield(runErrEvent(input.RunID, fmt.Errorf("convert messages: %w", err)), err)
		return
	}
	for event, iterErr := range rt.r.Stream(ctx, rt.a, runner.Messages(finoMsgs), runOpts...) {
		if susp, ok := event.(model.Suspended); ok && rt.store != nil {
			// Persist the runner-produced snapshot so a later resume restores the
			// exact authorized pending calls, never a client-forged history.
			if err := rt.store.Save(ctx, input.ThreadID, runner.SuspendedRunFrom(susp)); err != nil {
				yield(runErrEvent(input.RunID, fmt.Errorf("persist suspension: %w", err)), err)
				return
			}
		}
		mapped, mapErr := mapper.Map(event)
		if mapErr != nil {
			yield(runErrEvent(input.RunID, mapErr), mapErr)
			return
		}
		for _, ev := range mapped {
			if !yield(ev, nil) {
				return
			}
		}
		if iterErr != nil {
			return
		}
	}
}

// streamResume continues a run whose input.Resume is non-empty. It loads the
// snapshot the original suspension persisted (keyed by thread), converts
// ResumeEntry values to runner.Approval, calls runner.ResumeApproved, maps the
// post-resume tool results and assistant turns to AG-UI events, and emits a
// terminal RUN_FINISHED. Resume fails closed when no SuspendStore is configured
// or no snapshot exists for the thread, so a forged history cannot execute an
// unauthorized tool.
//
// Known adapter gaps (Phase 6 completeness audit):
//
//   - Handoff continuity: if the suspension occurred after a handoff, ResumeApproved
//     returns runner.ErrResumeAgentMismatch because this method always passes the
//     root agent (rt.a). The error surfaces as RUN_ERROR (fail-closed). Supporting
//     arbitrary handoff chains requires agent-tree resolution (missing core seam).
//
//   - Frontend-defined tools: AG-UI allows RunAgentInput.Tools to supply tool
//     definitions that the frontend executes. This path requires a Policy-initiated
//     DecisionSuspend before a SuspendedRun exists. Without Policy cooperation the
//     adapter cannot create one (missing core seam #1: external/deferred tool
//     execution from the design document).
func (rt *Runtime) streamResume(ctx context.Context, yield func(Event, error) bool, mapper *Mapper, resume []ResumeEntry, opts []runner.RunOption) {
	if rt.store == nil {
		yield(runErrEvent(mapper.runID, ErrResumeUnavailable), ErrResumeUnavailable)
		return
	}
	suspended, ok, err := rt.store.Load(ctx, mapper.threadID)
	if err != nil {
		err = fmt.Errorf("load suspended run: %w", err)
		yield(runErrEvent(mapper.runID, err), err)
		return
	}
	if !ok {
		err = fmt.Errorf("%w: %q", ErrNoSuspendedRun, mapper.threadID)
		yield(runErrEvent(mapper.runID, err), err)
		return
	}
	approvals := convertApprovals(resume)
	result, err := rt.r.ResumeApproved(ctx, rt.a, suspended, approvals, opts...)
	if err != nil {
		yield(runErrEvent(mapper.runID, err), err)
		return
	}
	for _, ev := range mapResumeResult(mapper, suspended, result) {
		if !yield(ev, nil) {
			return
		}
	}
	if result.Suspended {
		// A post-resume turn requested another tool the Policy suspended. Persist
		// the new snapshot under the same thread so the client can resume again,
		// and report an interrupt rather than a completion. The snapshot is kept,
		// not deleted.
		//
		// LastAgentName and LastMode are copied from the original suspended run,
		// not from result. This is correct today because ResumeApproved always
		// receives rt.a (the root agent), so the agent-name check at the top of
		// ResumeApproved prevents this code path from being reached after a
		// handoff to a sub-agent. If that constraint is ever relaxed (e.g. by
		// passing result.LastAgent instead of rt.a), LastAgentName and LastMode
		// must be taken from result rather than from the original suspended run.
		//
		// RunID is preserved from the original suspended run so idempotency keys
		// computed inside ResumeApproved remain stable across the resume chain
		// (loop-semantics I13).
		newSnap := runner.SuspendedRun{
			Messages:      result.Messages,
			LastAgentName: suspended.LastAgentName,
			LastMode:      suspended.LastMode,
			PendingCalls:  result.PendingCalls,
			RunID:         suspended.RunID,
		}
		if err = rt.store.Save(ctx, mapper.threadID, newSnap); err != nil {
			err = fmt.Errorf("persist re-suspension: %w", err)
			yield(runErrEvent(mapper.runID, err), err)
			return
		}
		yield(interruptFinishedEvent(mapper, result.PendingCalls), nil)
		return
	}
	// Completed: drop the snapshot so the thread cannot replay it. A delete
	// failure cannot undo a successful resume, so it must not surface as an error.
	_ = rt.store.Delete(ctx, mapper.threadID)
	yield(RunFinishedEvent{
		BaseEvent: BaseEvent{Type: EventRunFinished},
		ThreadID:  mapper.threadID,
		RunID:     mapper.runID,
	}, nil)
}

func runErrEvent(runID string, err error) RunErrorEvent {
	return RunErrorEvent{
		BaseEvent: BaseEvent{Type: EventRunError},
		Message:   err.Error(),
		RunID:     runID,
	}
}

// convertApprovals maps AG-UI resume entries to runner approvals. An entry
// approves its call only when the user resolved the interrupt without an
// explicit rejection: a "cancelled" status, or a resolved status whose payload
// carries {"approved": false}, both reject. AG-UI treats "resolved" as "the user
// responded", not "the user approved", so the rejection intent lives in the
// payload and must not be inferred from the status alone.
func convertApprovals(resume []ResumeEntry) []runner.Approval {
	approvals := make([]runner.Approval, len(resume))
	for i, e := range resume {
		approvals[i] = runner.Approval{
			CallID:   e.InterruptID,
			Approved: isApproved(e),
		}
	}
	return approvals
}

// isApproved reports whether a resume entry authorizes its tool call. Cancelled
// always rejects. A resolved entry rejects only when its payload explicitly says
// so via {"approved": false}; otherwise resolving the interrupt approves it.
func isApproved(e ResumeEntry) bool {
	if e.Status == ResumeStatusCancelled {
		return false
	}
	if payload, ok := e.Payload.(map[string]any); ok {
		if approved, ok := payload["approved"].(bool); ok {
			return approved
		}
	}
	return true
}

// convertMessages converts AG-UI messages to fino messages. System and
// developer messages are skipped. Consecutive RoleTool messages are grouped
// into a single fino ToolResults message to match fino's batch-result shape.
func convertMessages(msgs []Message) ([]message.Message, error) {
	var result []message.Message
	var toolBlocks []message.Block

	flush := func() {
		if len(toolBlocks) > 0 {
			result = append(result, message.ToolResults(toolBlocks...))
			toolBlocks = nil
		}
	}
	for _, m := range msgs {
		if m.Role == RoleTool {
			block, err := convertToolResult(m)
			if err != nil {
				return nil, err
			}
			toolBlocks = append(toolBlocks, block)
			continue
		}
		flush()
		converted, err := convertSingleMessage(m)
		if err != nil {
			return nil, err
		}
		if converted != nil {
			result = append(result, *converted)
		}
	}
	flush()
	return result, nil
}

func convertSingleMessage(m Message) (*message.Message, error) {
	switch m.Role {
	case RoleUser:
		if text := contentString(m.Content); text != "" {
			msg := message.UserText(text)
			return &msg, nil
		}
		return nil, nil
	case RoleAssistant:
		return convertAssistant(m)
	default:
		// RoleSystem, RoleDeveloper, RoleActivity, RoleReasoning, unknown: skip.
		// Developer messages are system-level configuration, not user turns.
		return nil, nil
	}
}

func convertAssistant(m Message) (*message.Message, error) {
	var blocks []message.Block
	if text := contentString(m.Content); text != "" {
		blocks = append(blocks, message.NewText(text))
	}
	for _, tc := range m.ToolCalls {
		input := json.RawMessage(tc.Function.Arguments)
		if len(input) == 0 {
			input = json.RawMessage("{}")
		}
		if !json.Valid(input) {
			return nil, fmt.Errorf("%w: tool call %q has invalid JSON arguments", ErrConvertMessage, tc.ID)
		}
		blocks = append(blocks, message.NewToolUse(tc.ID, tc.Function.Name, input))
	}
	if len(blocks) == 0 {
		return nil, nil
	}
	msg := message.Assistant(blocks...)
	return &msg, nil
}

func convertToolResult(m Message) (message.Block, error) {
	if m.ToolCallID == "" {
		return message.Block{}, fmt.Errorf("%w: tool result message has no toolCallId", ErrConvertMessage)
	}
	var content []message.Block
	if text := contentString(m.Content); text != "" {
		content = []message.Block{message.NewText(text)}
	}
	return message.NewToolResult(m.ToolCallID, m.Name, content, false), nil
}

// contentString extracts a string from an AG-UI message Content field. When
// JSON-decoded into any, string content arrives as a Go string; other types
// (arrays, objects) are not yet supported and return "".
func contentString(content any) string {
	s, _ := content.(string)
	return s
}
