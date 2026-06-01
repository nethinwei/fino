package runner

import (
	"context"
	"errors"
	"fmt"
	"iter"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/tool"
)

// emitErr reports a terminal error on the event stream: it fires the OnError
// hook and yields a model.StreamError alongside the non-nil iterator error.
func (r *Runner) emitErr(ctx context.Context, yield func(model.Event, error) bool, err error) {
	r.onError(ctx, err)
	yield(model.StreamError{Err: err}, err)
}

// Stream executes the ReAct loop like Run but yields semantic events as they
// occur. Iteration stops after a final message with no tool calls, on the turn
// limit, or on the first error. Terminal errors are yielded as a
// model.StreamError alongside a non-nil iterator error. The arguments mirror Run.
func (r *Runner) Stream(ctx context.Context, a *agent.Agent, input Input, opts ...RunOption) iter.Seq2[model.Event, error] {
	return func(yield func(model.Event, error) bool) {
		r.streamLoop(ctx, yield, a, input, opts)
	}
}

// streamLoop drives the ReAct loop for Stream, emitting events through yield
// until a final message, the turn limit, or an error ends iteration.
func (r *Runner) streamLoop(ctx context.Context, yield func(model.Event, error) bool, a *agent.Agent, input Input, opts []RunOption) {
	st, err := r.prepareRun(a, input, opts)
	if err != nil {
		r.emitErr(ctx, yield, err)
		return
	}
	for turn := 0; turn < r.maxTurns; turn++ {
		if err := ctx.Err(); err != nil {
			r.emitErr(ctx, yield, err)
			return
		}
		newCtx, msg, ok := r.streamGenerate(ctx, st, yield)
		if !ok {
			return
		}
		ctx = newCtx
		calls := msg.ToolUses()
		if len(calls) == 0 {
			yield(model.FinalMessage{Message: *msg}, nil)
			return
		}
		ctx, ok = r.streamToolCalls(ctx, st, calls, yield)
		if !ok {
			return
		}
	}
	r.emitErr(ctx, yield, fmt.Errorf("%w: %d", ErrMaxTurns, r.maxTurns))
}

// streamGenerate builds the model input, consumes the model's event stream
// (forwarding events and capturing the final message), fires the model hooks,
// and appends the response to history. The bool result is false when iteration
// should stop: a stream error, a stopped consumer, or a missing final message.
func (r *Runner) streamGenerate(ctx context.Context, st *runState, yield func(model.Event, error) bool) (context.Context, *message.Message, bool) {
	modelMessages := append([]message.Message{message.SystemText(st.mode.Instructions)}, st.history...)
	modelOpts := append([]model.Option(nil), st.mode.ModelOptions...)
	modelOpts = append(modelOpts, st.cfg.modelOpts...)
	infos, _ := collectTools(st.mode.Tools)

	ctx = r.beforeModel(ctx, st.agent.Name(), st.mode.Name, modelMessages, infos)

	var finalMsg *message.Message
	for event, err := range r.model.Stream(ctx, modelMessages, infos, modelOpts...) {
		if err != nil {
			r.emitErr(ctx, yield, err)
			return ctx, nil, false
		}
		if fm, ok := event.(model.FinalMessage); ok {
			msg := fm.Message
			finalMsg = &msg
			continue
		}
		if !yield(event, nil) {
			return ctx, nil, false
		}
	}
	if finalMsg == nil {
		r.emitErr(ctx, yield, errors.New("stream ended without final message"))
		return ctx, nil, false
	}
	r.afterModel(ctx, st.agent.Name(), st.mode.Name, finalMsg)
	st.history = append(st.history, *finalMsg)
	return ctx, finalMsg, true
}

// streamToolCalls authorizes and executes each requested tool serially,
// emitting ToolCall/ToolResult/Handoff events, then appends a single RoleTool
// message with all results. The bool result is false when iteration should stop.
func (r *Runner) streamToolCalls(ctx context.Context, st *runState, calls []message.ToolUse, yield func(model.Event, error) bool) (context.Context, bool) {
	_, toolsByName := collectTools(st.mode.Tools)
	blocks := make([]message.Block, 0, len(calls))
	for _, call := range calls {
		if err := ctx.Err(); err != nil {
			r.emitErr(ctx, yield, err)
			return ctx, false
		}
		newCtx, block, ok := r.handleStreamToolCall(ctx, st, toolsByName, call, yield)
		if !ok {
			return ctx, false
		}
		ctx = newCtx
		blocks = append(blocks, block)
	}
	st.history = append(st.history, message.ToolResults(blocks...))
	return ctx, true
}

// handleStreamToolCall resolves, authorizes, and executes one tool call,
// emitting ToolCall and ToolResult events around execution and returning the
// tool_result block. A handoff tool additionally switches the run state to the
// target agent and emits a Handoff event.
func (r *Runner) handleStreamToolCall(ctx context.Context, st *runState, toolsByName map[string]tool.Tool, call message.ToolUse, yield func(model.Event, error) bool) (context.Context, message.Block, bool) {
	selected, ok := toolsByName[call.Name]
	if !ok {
		r.emitErr(ctx, yield, fmt.Errorf("%w: %q", ErrToolNotFound, call.Name))
		return ctx, message.Block{}, false
	}
	if err := r.authorize(ctx, st, selected, call); err != nil {
		r.emitErr(ctx, yield, err)
		return ctx, message.Block{}, false
	}
	if !yield(model.ToolCall{Call: call}, nil) {
		return ctx, message.Block{}, false
	}
	ctx, out, err := r.execute(ctx, st, selected, call)
	if err != nil {
		r.emitErr(ctx, yield, err)
		return ctx, message.Block{}, false
	}
	if !yield(model.ToolResult{CallID: call.ID, Name: call.Name, Result: out}, nil) {
		return ctx, message.Block{}, false
	}
	block := message.NewToolResult(call.ID, call.Name, out.Content, out.IsError)
	if handoff, isHandoff := selected.(agent.HandoffTool); isHandoff {
		if err := st.switchTo(handoff); err != nil {
			r.emitErr(ctx, yield, err)
			return ctx, message.Block{}, false
		}
		if !yield(model.Handoff{Target: st.agent.Name()}, nil) {
			return ctx, message.Block{}, false
		}
	}
	return ctx, block, true
}
