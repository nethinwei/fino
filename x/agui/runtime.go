package agui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/runner"
	"github.com/nethinwei/fino/tool"
)

// Errors returned by Runtime construction and message conversion.
var (
	ErrMissingRunner  = errors.New("runner is required")
	ErrMissingAgent   = errors.New("agent is required")
	ErrConvertMessage = errors.New("cannot convert AG-UI message")
)

// Runtime bridges a RunAgentInput to fino runner.Stream and maps the resulting
// fino events into AG-UI events using a Mapper.
type Runtime struct {
	r *runner.Runner
	a *agent.Agent
}

// NewRuntime creates a Runtime from a configured Runner and Agent.
func NewRuntime(r *runner.Runner, a *agent.Agent) (*Runtime, error) {
	if r == nil {
		return nil, ErrMissingRunner
	}
	if a == nil {
		return nil, ErrMissingAgent
	}
	return &Runtime{r: r, a: a}, nil
}

// Stream returns an iterator over AG-UI events for one run. The first event is
// always RUN_STARTED; the last is RUN_FINISHED (success or interrupt) or
// RUN_ERROR. When input.Resume is non-empty the run is treated as a resume
// from a prior suspension; see buildSuspendedRun for constraints.
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
	finoMsgs, err := convertMessages(input.Messages)
	if err != nil {
		yield(runErrEvent(input.RunID, fmt.Errorf("convert messages: %w", err)), err)
		return
	}
	runOpts := append([]runner.RunOption{runner.WithRunID(input.RunID)}, opts...)
	if len(input.Resume) > 0 {
		rt.streamResume(ctx, yield, mapper, finoMsgs, input.Resume, runOpts)
		return
	}
	for event, iterErr := range rt.r.Stream(ctx, rt.a, runner.Messages(finoMsgs), runOpts...) {
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

// streamResume handles a run where input.Resume is non-empty. It reconstructs
// a runner.SuspendedRun from the message history, converts ResumeEntry values
// to runner.Approval, calls runner.ResumeApproved, and emits RUN_FINISHED.
// ResumeApproved does not stream; only the terminal event is emitted. opts
// that would apply to post-resume turns (e.g. WithModelOptions) are not
// forwarded because runner.ResumeApproved enforces LastMode and the streaming
// resume seam does not yet exist (see spec "Likely core seams" #1).
func (rt *Runtime) streamResume(ctx context.Context, yield func(Event, error) bool, mapper *Mapper, finoMsgs []message.Message, resume []ResumeEntry, opts []runner.RunOption) {
	suspended, err := buildSuspendedRun(rt.a, finoMsgs)
	if err != nil {
		yield(runErrEvent(mapper.runID, fmt.Errorf("rebuild suspended run: %w", err)), err)
		return
	}
	approvals := convertApprovals(resume)
	// Pass non-mode opts (e.g. WithModelOptions) to ResumeApproved so callers
	// can influence post-resume model behavior. WithMode inside opts is rejected
	// by ResumeApproved when it conflicts with suspended.LastMode.
	if _, err = rt.r.ResumeApproved(ctx, rt.a, suspended, approvals, opts...); err != nil {
		yield(runErrEvent(mapper.runID, err), err)
		return
	}
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

// buildSuspendedRun reconstructs a runner.SuspendedRun from the AG-UI message
// history and the agent's current default mode. The mode used is always the
// agent's default mode; if a non-default mode was active at suspend time, pass
// runner.WithMode on the original run so the mode names align.
func buildSuspendedRun(a *agent.Agent, msgs []message.Message) (runner.SuspendedRun, error) {
	modeName := a.DefaultMode()
	mode, ok := a.Mode(modeName)
	if !ok {
		return runner.SuspendedRun{}, fmt.Errorf("agent %q has no mode %q", a.Name(), modeName)
	}
	if len(msgs) == 0 {
		return runner.SuspendedRun{}, errors.New("message history is empty")
	}
	last := msgs[len(msgs)-1]
	if last.Role != message.RoleAssistant {
		return runner.SuspendedRun{}, errors.New("last message is not an assistant message")
	}
	calls := last.ToolUses()
	if len(calls) == 0 {
		return runner.SuspendedRun{}, errors.New("last assistant message has no tool calls")
	}
	byName := make(map[string]tool.Tool, len(mode.Tools))
	for _, t := range mode.Tools {
		if t != nil {
			byName[t.Info().Name] = t
		}
	}
	pending := make([]runner.PendingToolCall, len(calls))
	for i, call := range calls {
		t, ok := byName[call.Name]
		if !ok {
			return runner.SuspendedRun{}, fmt.Errorf("tool %q not found in agent mode %q", call.Name, modeName)
		}
		pending[i] = runner.PendingToolCall{Tool: t.Info(), Call: call, Reason: "resumed via AG-UI"}
	}
	return runner.SuspendedRun{
		Messages:      msgs,
		LastAgentName: a.Name(),
		LastMode:      modeName,
		PendingCalls:  pending,
	}, nil
}

func convertApprovals(resume []ResumeEntry) []runner.Approval {
	approvals := make([]runner.Approval, len(resume))
	for i, e := range resume {
		approvals[i] = runner.Approval{
			CallID:   e.InterruptID,
			Approved: e.Status == ResumeStatusResolved,
		}
	}
	return approvals
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
	case RoleUser, RoleDeveloper:
		if text := contentString(m.Content); text != "" {
			msg := message.UserText(text)
			return &msg, nil
		}
		return nil, nil
	case RoleAssistant:
		return convertAssistant(m)
	default:
		// RoleSystem, RoleActivity, RoleReasoning, unknown: skip.
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
