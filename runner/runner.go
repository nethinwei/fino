// Package runner executes the ReAct feedback loop for the fino Agent SDK. A
// Runner holds only configuration; each run owns its own message list, so a
// single Runner can be reused concurrently across independent runs.
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/hooks"
	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/policy"
	"github.com/nethinwei/fino/tool"
)

var (
	// ErrMaxTurns indicates the run exceeded the configured maximum number of turns.
	ErrMaxTurns = errors.New("max turns exceeded")
	// ErrToolNotFound indicates the model requested a tool the current mode does not provide.
	ErrToolNotFound = errors.New("tool not found")
	// ErrSystemMessageInHistory indicates the run input contained a system
	// message; the Runner injects system instructions from the mode instead.
	ErrSystemMessageInHistory = errors.New("system message in history")
	// ErrToolDenied is the sentinel wrapped by ToolDeniedError when a Policy denies a tool call.
	ErrToolDenied = errors.New("tool denied")
)

// ToolDeniedError reports that a Policy denied a tool invocation. It wraps
// ErrToolDenied and carries the denied tool's Info and the Policy Decision.
type ToolDeniedError struct {
	Tool     tool.Info
	Decision policy.Decision
}

// Error implements the error interface.
func (e *ToolDeniedError) Error() string {
	return fmt.Sprintf("tool %q denied: %s", e.Tool.Name, e.Decision.Reason)
}

// Unwrap returns ErrToolDenied so errors.Is(err, ErrToolDenied) reports true.
func (e *ToolDeniedError) Unwrap() error {
	return ErrToolDenied
}

// Runner executes the ReAct loop against an Agent. It holds only configuration
// (model, turn limit, policy, hooks); each Run owns its own message list.
type Runner struct {
	model          model.Model
	maxTurns       int
	policy         policy.Policy
	hooks          *hooks.Hooks
	maxConcurrency int
}

// Option configures a Runner.
type Option func(*Runner)

// New creates a Runner with the given model and options. It returns an error if
// the model is nil or the configured maximum turns is not positive. The
// defaults are 10 turns and an AllowAll policy.
func New(m model.Model, opts ...Option) (*Runner, error) {
	if m == nil {
		return nil, errors.New("model is required")
	}
	r := &Runner{model: m, maxTurns: 10, policy: policy.AllowAll{}}
	for _, opt := range opts {
		if opt != nil {
			opt(r)
		}
	}
	if r.maxTurns <= 0 {
		return nil, errors.New("max turns must be positive")
	}
	return r, nil
}

// WithMaxTurns sets the maximum number of model turns per run.
func WithMaxTurns(n int) Option { return func(r *Runner) { r.maxTurns = n } }

// WithPolicy sets the authorization policy consulted before each tool call. A
// nil policy is ignored, preserving the default AllowAll.
func WithPolicy(p policy.Policy) Option {
	return func(r *Runner) {
		if p != nil {
			r.policy = p
		}
	}
}

// WithHooks sets the lifecycle hooks observed during a run.
func WithHooks(h *hooks.Hooks) Option { return func(r *Runner) { r.hooks = h } }

// WithMaxConcurrency sets the maximum number of tools executed concurrently
// within a single tool-call batch. A value of n <= 1 (the default) keeps tools
// serial. A value of n > 1 executes up to n tools at once: the Runner still
// authorizes all calls serially in call order, preserves result order, and is
// fail-fast on the first error; user tools must be safe for concurrent use.
func WithMaxConcurrency(n int) Option { return func(r *Runner) { r.maxConcurrency = n } }

// Input is the initial message list for a run. Construct it with Text or
// Messages rather than building it directly.
type Input struct {
	messages []message.Message
}

// Text returns an Input containing a single user text message.
func Text(text string) Input {
	return Input{messages: []message.Message{message.UserText(text)}}
}

// Messages returns an Input from an existing message history. The slice is
// copied, so later caller mutations do not affect the run.
func Messages(messages []message.Message) Input {
	return Input{messages: append([]message.Message(nil), messages...)}
}

// Result is the outcome of a completed run.
type Result struct {
	Message   message.Message
	Messages  []message.Message
	LastAgent *agent.Agent
	LastMode  string
}

// Text returns the text of the final message.
func (r *Result) Text() string { return r.Message.Text() }

// RunOption configures a single run.
type RunOption func(*runConfig)

type runConfig struct {
	modeName  string
	modelOpts []model.Option
}

// WithMode selects which mode of the Agent to start the run in. The default is
// the Agent's default mode.
func WithMode(name string) RunOption { return func(c *runConfig) { c.modeName = name } }

// WithModelOptions appends model options applied after the mode's own defaults.
func WithModelOptions(opts ...model.Option) RunOption {
	return func(c *runConfig) { c.modelOpts = append(c.modelOpts, opts...) }
}

// runState holds the mutable state of a single run: the active agent and its
// mode, the accumulated message history, and the resolved per-run config. Each
// Run or Stream owns one runState; the Runner itself stays immutable.
type runState struct {
	agent   *agent.Agent
	mode    agent.Mode
	history []message.Message
	cfg     runConfig
}

// switchTo points the run state at the handoff target's agent and its default
// mode. It returns an error if the target's default mode is missing.
func (st *runState) switchTo(handoff agent.HandoffTool) error {
	target := handoff.TargetAgent()
	mode, ok := target.Mode(target.DefaultMode())
	if !ok {
		return fmt.Errorf("target agent %q default mode %q not found", target.Name(), target.DefaultMode())
	}
	st.agent = target
	st.mode = mode
	return nil
}

// applyHandoffs switches the run state for each handoff tool in the batch, in
// call order, so the last handoff wins. This matches the serial path, where
// each handoff's switchTo overwrites the previous one.
func (st *runState) applyHandoffs(selected []tool.Tool) error {
	for _, t := range selected {
		handoff, ok := t.(agent.HandoffTool)
		if !ok {
			continue
		}
		if err := st.switchTo(handoff); err != nil {
			return err
		}
	}
	return nil
}

// result builds the final Result from the run state and terminating message.
func (st *runState) result(msg *message.Message) *Result {
	return &Result{
		Message:   *msg,
		Messages:  st.history,
		LastAgent: st.agent,
		LastMode:  st.mode.Name,
	}
}

// prepareRun validates the inputs, resolves the starting mode, and returns the
// initial run state. It is shared by Run and Stream.
func (r *Runner) prepareRun(a *agent.Agent, input Input, opts []RunOption) (*runState, error) {
	if a == nil {
		return nil, errors.New("agent is required")
	}
	if message.HasSystem(input.messages) {
		return nil, ErrSystemMessageInHistory
	}
	cfg := runConfig{modeName: a.DefaultMode()}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	mode, ok := a.Mode(cfg.modeName)
	if !ok {
		return nil, fmt.Errorf("mode %q not found", cfg.modeName)
	}
	return &runState{
		agent:   a,
		mode:    mode,
		history: append([]message.Message(nil), input.messages...),
		cfg:     cfg,
	}, nil
}

// authorize consults the policy for a single tool call. It returns a
// *ToolDeniedError when the policy denies the call, or the policy's own error
// when the policy itself fails. It is shared by Run and Stream; callers report
// the error via OnError or the event stream.
func (r *Runner) authorize(ctx context.Context, st *runState, selected tool.Tool, call message.ToolUse) error {
	decision, err := r.policy.Authorize(ctx, policy.Request{
		AgentName: st.agent.Name(),
		ModeName:  st.mode.Name,
		Tool:      selected.Info(),
		Input:     call.Input,
	})
	if err != nil {
		return err
	}
	if !decision.Allow {
		return &ToolDeniedError{Tool: selected.Info(), Decision: decision}
	}
	return nil
}

// execute runs a single authorized tool, firing the BeforeTool and AfterTool
// hooks around it. It returns the (possibly hook-updated) context so callers
// can propagate it. It is shared by Run and Stream.
func (r *Runner) execute(ctx context.Context, st *runState, selected tool.Tool, call message.ToolUse) (context.Context, tool.Result, error) {
	ctx = r.beforeTool(ctx, st.agent.Name(), st.mode.Name, selected.Info(), call.Input)
	out, err := selected.Run(ctx, call.Input)
	if err != nil {
		return ctx, tool.Result{}, err
	}
	r.afterTool(ctx, st.agent.Name(), st.mode.Name, selected.Info(), out)
	return ctx, out, nil
}

// Run executes the ReAct loop until the model returns a message with no tool
// calls, the turn limit is reached, or an error occurs. It returns an error if
// the agent is nil, the input contains a system message, or the selected mode
// is not found.
func (r *Runner) Run(ctx context.Context, a *agent.Agent, input Input, opts ...RunOption) (*Result, error) {
	st, err := r.prepareRun(a, input, opts)
	if err != nil {
		return nil, err
	}
	for turn := 0; turn < r.maxTurns; turn++ {
		if err := ctx.Err(); err != nil {
			r.onError(ctx, err)
			return nil, err
		}
		newCtx, msg, err := r.generate(ctx, st)
		if err != nil {
			return nil, err
		}
		ctx = newCtx
		calls := msg.ToolUses()
		if len(calls) == 0 {
			return st.result(msg), nil
		}
		ctx, err = r.runToolCalls(ctx, st, calls)
		if err != nil {
			return nil, err
		}
	}
	err = fmt.Errorf("%w: %d", ErrMaxTurns, r.maxTurns)
	r.onError(ctx, err)
	return nil, err
}

// generate builds the model input from the run state, invokes the model, fires
// the BeforeModel and AfterModel hooks, and appends the response to history. It
// returns the hook-updated context and the model's message.
func (r *Runner) generate(ctx context.Context, st *runState) (context.Context, *message.Message, error) {
	modelMessages := append([]message.Message{message.SystemText(st.mode.Instructions)}, st.history...)
	modelOpts := append([]model.Option(nil), st.mode.ModelOptions...)
	modelOpts = append(modelOpts, st.cfg.modelOpts...)
	infos, _ := collectTools(st.mode.Tools)

	ctx = r.beforeModel(ctx, st.agent.Name(), st.mode.Name, modelMessages, infos)
	msg, err := r.model.Generate(ctx, modelMessages, infos, modelOpts...)
	if err != nil {
		r.onError(ctx, err)
		return ctx, nil, err
	}
	r.afterModel(ctx, st.agent.Name(), st.mode.Name, msg)
	st.history = append(st.history, *msg)
	return ctx, msg, nil
}

// runToolCalls authorizes and executes each requested tool serially, then
// appends a single RoleTool message holding all results to the run history.
// When maxConcurrency > 1 and the batch has more than one call, it delegates
// to runToolCallsParallel.
func (r *Runner) runToolCalls(ctx context.Context, st *runState, calls []message.ToolUse) (context.Context, error) {
	if r.maxConcurrency > 1 && len(calls) > 1 {
		return r.runToolCallsParallel(ctx, st, calls)
	}
	_, toolsByName := collectTools(st.mode.Tools)
	blocks := make([]message.Block, 0, len(calls))
	for _, call := range calls {
		if err := ctx.Err(); err != nil {
			r.onError(ctx, err)
			return ctx, err
		}
		newCtx, block, err := r.handleToolCall(ctx, st, toolsByName, call)
		if err != nil {
			return ctx, err
		}
		ctx = newCtx
		blocks = append(blocks, block)
	}
	st.history = append(st.history, message.ToolResults(blocks...))
	return ctx, nil
}

// handleToolCall resolves, authorizes, and executes one tool call, returning
// its tool_result block. A handoff tool additionally switches the run state to
// the target agent after its result is recorded.
func (r *Runner) handleToolCall(ctx context.Context, st *runState, toolsByName map[string]tool.Tool, call message.ToolUse) (context.Context, message.Block, error) {
	selected, ok := toolsByName[call.Name]
	if !ok {
		err := fmt.Errorf("%w: %q", ErrToolNotFound, call.Name)
		r.onError(ctx, err)
		return ctx, message.Block{}, err
	}
	if err := r.authorize(ctx, st, selected, call); err != nil {
		r.onError(ctx, err)
		return ctx, message.Block{}, err
	}
	ctx, out, err := r.execute(ctx, st, selected, call)
	if err != nil {
		r.onError(ctx, err)
		return ctx, message.Block{}, err
	}
	block := message.NewToolResult(call.ID, call.Name, out.Content, out.IsError)
	if handoff, isHandoff := selected.(agent.HandoffTool); isHandoff {
		if err := st.switchTo(handoff); err != nil {
			return ctx, message.Block{}, err
		}
	}
	return ctx, block, nil
}

// authorizeBatch resolves and authorizes every call serially in call order.
// Resolving and authorizing stay serial so Policy ordering is deterministic and
// the batch fails fast on the first missing or denied tool, exactly as the
// serial path does. It returns the selected tools indexed by call order.
func (r *Runner) authorizeBatch(ctx context.Context, st *runState, toolsByName map[string]tool.Tool, calls []message.ToolUse) ([]tool.Tool, error) {
	selected := make([]tool.Tool, len(calls))
	for i, call := range calls {
		t, ok := toolsByName[call.Name]
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrToolNotFound, call.Name)
		}
		if err := r.authorize(ctx, st, t, call); err != nil {
			return nil, err
		}
		selected[i] = t
	}
	return selected, nil
}

// toolOutcome is the result of one parallel tool execution, kept at its call
// index so results stay in call order regardless of completion order.
type toolOutcome struct {
	out tool.Result
	err error
}

// runToolCallsParallel authorizes all calls serially, executes the authorized
// tools concurrently (bounded by maxConcurrency), appends one RoleTool message
// with results in call order, and applies handoffs in call order. The first
// error by call order cancels siblings and is returned.
func (r *Runner) runToolCallsParallel(ctx context.Context, st *runState, calls []message.ToolUse) (context.Context, error) {
	_, toolsByName := collectTools(st.mode.Tools)
	selected, err := r.authorizeBatch(ctx, st, toolsByName, calls)
	if err != nil {
		r.onError(ctx, err)
		return ctx, err
	}
	outcomes := r.executeParallel(ctx, st, selected, calls)
	for _, oc := range outcomes {
		if oc.err != nil {
			r.onError(ctx, oc.err)
			return ctx, oc.err
		}
	}
	if err := st.applyHandoffs(selected); err != nil {
		return ctx, err
	}
	st.history = append(st.history, message.ToolResults(resultBlocks(calls, outcomes)...))
	return ctx, nil
}

// executeParallel runs every selected tool in its own goroutine, bounded by a
// maxConcurrency-sized semaphore. The first error cancels a derived context so
// ctx-aware siblings stop early. Results are written back at their call index.
func (r *Runner) executeParallel(ctx context.Context, st *runState, selected []tool.Tool, calls []message.ToolUse) []toolOutcome {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	outcomes := make([]toolOutcome, len(calls))
	sem := make(chan struct{}, r.maxConcurrency)
	var wg sync.WaitGroup
	for i := range calls {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := ctx.Err(); err != nil {
				outcomes[i] = toolOutcome{err: err}
				return
			}
			_, out, err := r.execute(ctx, st, selected[i], calls[i])
			outcomes[i] = toolOutcome{out: out, err: err}
			if err != nil {
				cancel()
			}
		}(i)
	}
	wg.Wait()
	return outcomes
}

// resultBlocks builds tool_result blocks in call order from parallel outcomes.
func resultBlocks(calls []message.ToolUse, outcomes []toolOutcome) []message.Block {
	blocks := make([]message.Block, len(calls))
	for i, call := range calls {
		blocks[i] = message.NewToolResult(call.ID, call.Name, outcomes[i].out.Content, outcomes[i].out.IsError)
	}
	return blocks
}

func collectTools(tools []tool.Tool) ([]tool.Info, map[string]tool.Tool) {
	infos := make([]tool.Info, 0, len(tools))
	byName := map[string]tool.Tool{}
	for _, t := range tools {
		if t == nil {
			continue
		}
		info := t.Info()
		infos = append(infos, info)
		byName[info.Name] = t
	}
	return infos, byName
}

func (r *Runner) beforeModel(ctx context.Context, agentName, modeName string, messages []message.Message, tools []tool.Info) context.Context {
	if r.hooks != nil && r.hooks.BeforeModel != nil {
		return r.hooks.BeforeModel(ctx, hooks.ModelCall{AgentName: agentName, ModeName: modeName, Messages: messages, Tools: tools})
	}
	return ctx
}

func (r *Runner) afterModel(ctx context.Context, agentName, modeName string, msg *message.Message) {
	if r.hooks != nil && r.hooks.AfterModel != nil {
		r.hooks.AfterModel(ctx, hooks.ModelResult{AgentName: agentName, ModeName: modeName, Message: msg})
	}
}

func (r *Runner) beforeTool(ctx context.Context, agentName, modeName string, info tool.Info, input json.RawMessage) context.Context {
	if r.hooks != nil && r.hooks.BeforeTool != nil {
		return r.hooks.BeforeTool(ctx, hooks.ToolCall{AgentName: agentName, ModeName: modeName, Tool: info, Input: input})
	}
	return ctx
}

func (r *Runner) afterTool(ctx context.Context, agentName, modeName string, info tool.Info, out tool.Result) {
	if r.hooks != nil && r.hooks.AfterTool != nil {
		r.hooks.AfterTool(ctx, hooks.ToolResult{AgentName: agentName, ModeName: modeName, Tool: info, Result: out})
	}
}

func (r *Runner) onError(ctx context.Context, err error) {
	if r.hooks != nil && r.hooks.OnError != nil {
		r.hooks.OnError(ctx, err)
	}
}
