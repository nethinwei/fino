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
	// ErrStreamContract indicates a model.Model.Stream implementation violated
	// its event contract: it must yield exactly one TurnMessage per turn and must
	// not yield FinalMessage (FinalMessage is emitted only by the Runner).
	ErrStreamContract = errors.New("model stream contract violated")
	// ErrInvalidToolCallID indicates a tool_use block carried an empty ID. The
	// Runner rejects the batch before authorizing or executing any tool, because
	// approval, the idempotency key, and the replay tape all key on the ID.
	ErrInvalidToolCallID = errors.New("invalid tool call id")
	// ErrDuplicateToolCallID indicates two tool_use blocks in the same batch
	// shared an ID, which makes approval matching and the idempotency key
	// ambiguous. The Runner rejects the batch before executing any tool.
	ErrDuplicateToolCallID = errors.New("duplicate tool call id")
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

// WithMaxConcurrency sets the maximum number of tools the Runner may execute
// concurrently within a single tool-call batch. A value of n <= 1 (the default)
// keeps tools serial. A value of n > 1 permits up to n tools at once only when
// every tool in the batch declares tool.Effects.ParallelSafe; otherwise the
// whole batch falls back to serial execution. The Runner still authorizes all
// calls serially in call order, preserves result order, and is fail-fast on the
// first error.
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

// Result is the outcome of a run. A run terminates in one of two successful
// shapes: a completed run (Suspended == false) carries the final Message; a
// suspended run (Suspended == true) carries PendingCalls and leaves Message
// zero. The two are mutually exclusive (loop-semantics I3).
type Result struct {
	Message   message.Message
	Messages  []message.Message
	LastAgent *agent.Agent
	LastMode  string
	// Suspended reports that a Policy suspended the run before executing a tool
	// batch. The history tail is the dangling assistant tool_use message.
	Suspended bool
	// PendingCalls holds the suspended tool calls (only those whose decision
	// resolved to DecisionSuspend), in call order. It is non-empty only when
	// Suspended is true.
	PendingCalls []PendingToolCall
	// runID is the run-scoped identifier (runner.WithRunID), carried so
	// SuspendedRun can restore it for ResumeApproved. Unexported because callers
	// read it through SuspendedRun, not off the Result.
	runID string
}

// Text returns the text of the final message.
func (r *Result) Text() string { return r.Message.Text() }

// PendingToolCall describes a tool call that a Policy suspended. It carries the
// tool's static info, the original model tool_use, and the Policy's reason, so a
// caller can present the call for human approval and later resume it.
type PendingToolCall struct {
	Tool   tool.Info
	Call   message.ToolUse
	Reason string
}

// RunOption configures a single run.
type RunOption func(*runConfig)

type runConfig struct {
	modeName      string
	modelOpts     []model.Option
	resumePending bool
	runID         string
}

// WithMode selects which mode of the Agent to start the run in. The default is
// the Agent's default mode.
func WithMode(name string) RunOption { return func(c *runConfig) { c.modeName = name } }

// WithRunID sets the run-scoped identifier surfaced to tools via
// tool.ExecutionContext. It is shared by Run, Stream, and ResumeApproved. When
// omitted, the run ID is empty and the derived IdempotencyKey is also empty,
// which preserves prior behavior exactly.
func WithRunID(id string) RunOption { return func(c *runConfig) { c.runID = id } }

// WithModelOptions appends model options applied after the mode's own defaults.
func WithModelOptions(opts ...model.Option) RunOption {
	return func(c *runConfig) { c.modelOpts = append(c.modelOpts, opts...) }
}

// WithResumeFromPendingTools makes a run first execute any pending tool calls at
// the tail of the input history before calling the model. Pending calls are the
// tool_uses of the last message when it is an assistant message that carries at
// least one tool_use and therefore has no following tool_result message.
//
// This is the minimal seam for mid-batch / human-in-the-loop resume identified
// by invariant I10 (see docs/spec/loop-semantics.md §7.2): it exposes the
// ability to continue a run that was interrupted after the model requested tools
// but before they ran (e.g. while awaiting human approval), without introducing
// any checkpoint, session, or graph concept. When the seam is off (the default)
// or the tail has no pending tool_use, it is a no-op and the run starts at the
// model as usual.
func WithResumeFromPendingTools() RunOption {
	return func(c *runConfig) { c.resumePending = true }
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

// suspendedResult builds a suspended Result from the run state and the batch's
// pending (suspended) calls. The history already ends with the dangling
// assistant tool_use message, and no tool_result has been appended.
func (st *runState) suspendedResult(pending []PendingToolCall) *Result {
	return &Result{
		Messages:     st.history,
		LastAgent:    st.agent,
		LastMode:     st.mode.Name,
		Suspended:    true,
		PendingCalls: pending,
		runID:        st.cfg.runID,
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

// authorize consults the policy for a single tool call. It returns the policy's
// Decision plus an error: the policy's own error when the policy system fails,
// or a *ToolDeniedError when the decision resolves to DecisionDeny. An Allow or
// Suspend decision returns a nil error; callers inspect decision.ResolvedKind()
// to tell allow from suspend. It is shared by Run and Stream.
func (r *Runner) authorize(ctx context.Context, st *runState, selected tool.Tool, call message.ToolUse) (policy.Decision, error) {
	decision, err := r.policy.Authorize(ctx, policy.Request{
		AgentName: st.agent.Name(),
		ModeName:  st.mode.Name,
		Tool:      selected.Info(),
		Input:     call.Input,
	})
	if err != nil {
		return decision, err
	}
	if decision.ResolvedKind() == policy.DecisionDeny {
		return decision, &ToolDeniedError{Tool: selected.Info(), Decision: decision}
	}
	return decision, nil
}

// execute runs a single authorized tool, firing the BeforeTool and AfterTool
// hooks around it. It first injects the run-scoped tool.ExecutionContext so
// both lifecycle hooks and the tool itself can read it (loop-semantics I13). It
// returns the (possibly hook-updated) context so callers can propagate it. It
// is shared by Run, Stream, and ResumeApproved.
func (r *Runner) execute(ctx context.Context, st *runState, selected tool.Tool, call message.ToolUse) (context.Context, tool.Result, error) {
	ctx = tool.ContextWithExecutionContext(ctx, executionContext(st.cfg.runID, call.ID))
	ctx = r.beforeTool(ctx, st.agent.Name(), st.mode.Name, selected.Info(), call.Input)
	out, err := selected.Run(ctx, call.Input)
	if err != nil {
		return ctx, tool.Result{}, err
	}
	r.afterTool(ctx, st.agent.Name(), st.mode.Name, selected.Info(), out)
	return ctx, out, nil
}

// executionContext builds the per-call tool.ExecutionContext. The
// IdempotencyKey is a deterministic function of (runID, toolCallID): runID +
// ":" + toolCallID when runID is non-empty, otherwise empty. The derivation is
// an implementation detail; tools read IdempotencyKey, they do not build it.
func executionContext(runID, toolCallID string) tool.ExecutionContext {
	key := ""
	if runID != "" {
		key = runID + ":" + toolCallID
	}
	return tool.ExecutionContext{RunID: runID, ToolCallID: toolCallID, IdempotencyKey: key}
}

// pendingToolUses returns the pending tool calls at the tail of history: the
// tool_uses of the last message when it is an assistant message carrying at
// least one tool_use. Because such a message is last, it has no following
// tool_result message, so all its tool_uses are unresolved. The detection does
// not require the assistant message to contain only tool_use blocks (real
// providers interleave thinking/text with tool_use). It returns nil otherwise.
func pendingToolUses(history []message.Message) []message.ToolUse {
	if len(history) == 0 {
		return nil
	}
	last := history[len(history)-1]
	if last.Role != message.RoleAssistant {
		return nil
	}
	return last.ToolUses()
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
	ctx, pending, err := r.resumePendingTools(ctx, st)
	if err != nil {
		return nil, err
	}
	if len(pending) > 0 {
		return st.suspendedResult(pending), nil
	}
	return r.loop(ctx, st)
}

// loop drives the ReAct turns from the current run state: generate, and if the
// model requested tools, run the batch and append its results, until the model
// returns no tool calls (completed), a batch suspends (suspended Result), the
// turn limit is reached, or an error occurs. It is the shared continuation used
// by Run and by ResumeApproved after the resumed batch is appended.
func (r *Runner) loop(ctx context.Context, st *runState) (*Result, error) {
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
		var pending []PendingToolCall
		ctx, pending, err = r.runToolCalls(ctx, st, calls)
		if err != nil {
			return nil, err
		}
		if len(pending) > 0 {
			return st.suspendedResult(pending), nil
		}
	}
	err := fmt.Errorf("%w: %d", ErrMaxTurns, r.maxTurns)
	r.onError(ctx, err)
	return nil, err
}

// resumePendingTools executes the input history's tail pending tool calls before
// the loop, when the resume-from-pending seam is enabled. It is a no-op when the
// seam is off or the tail has no pending tool_use. Executing the pending batch
// appends its single tool_result message, so the following model turn observes a
// well-formed history and the ReAct loop continues normally.
func (r *Runner) resumePendingTools(ctx context.Context, st *runState) (context.Context, []PendingToolCall, error) {
	if !st.cfg.resumePending {
		return ctx, nil, nil
	}
	pending := pendingToolUses(st.history)
	if len(pending) == 0 {
		return ctx, nil, nil
	}
	if err := ctx.Err(); err != nil {
		r.onError(ctx, err)
		return ctx, nil, err
	}
	return r.runToolCalls(ctx, st, pending)
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

// runToolCalls authorizes the whole batch first, then executes it. Authorizing
// before any execution makes the serial and parallel paths equivalent and
// ensures that a deny or suspend anywhere in the batch produces no side effects
// (loop-semantics §4, I6). It returns the suspended pending calls (non-empty
// only when the batch suspended, in which case nothing executed) so the caller
// can halt with a suspended Result. When maxConcurrency > 1 and every selected
// tool declares ParallelSafe, it delegates to the parallel finish path.
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
	// Apply handoffs only after the whole batch executes, in call order, so all
	// tools in the batch run under the pre-handoff mode and the serial path
	// matches the parallel path (loop-semantics §4 equivalence; handoff is
	// batch-terminal).
	if err := st.applyHandoffs(selected); err != nil {
		return ctx, nil, err
	}
	st.history = append(st.history, message.ToolResults(blocks...))
	return ctx, nil, nil
}

// executeBatchSerial runs every authorized tool in call order, returning the
// tool_result blocks. It fires OnError on the first execution failure.
func (r *Runner) executeBatchSerial(ctx context.Context, st *runState, selected []tool.Tool, calls []message.ToolUse) (context.Context, []message.Block, error) {
	blocks := make([]message.Block, 0, len(calls))
	for i, call := range calls {
		if err := ctx.Err(); err != nil {
			r.onError(ctx, err)
			return ctx, nil, err
		}
		newCtx, out, err := r.execute(ctx, st, selected[i], call)
		if err != nil {
			r.onError(ctx, err)
			return ctx, nil, err
		}
		ctx = newCtx
		blocks = append(blocks, message.NewToolResult(call.ID, call.Name, out.Content, out.IsError))
	}
	return ctx, blocks, nil
}

// validateToolCallIDs enforces the tool-use ID invariants for a batch: every ID
// must be non-empty and unique within the batch. Approval matching
// (Approval.CallID), the idempotency key (RunID:ToolCallID), and the replay tape
// all key on the ID, so the Runner turns the provider's "unique non-empty IDs"
// expectation into a checked invariant rather than a silent assumption. It runs
// before any resolve, authorize, or execute, so a malformed batch produces no
// side effects.
func validateToolCallIDs(calls []message.ToolUse) error {
	seen := make(map[string]bool, len(calls))
	for _, c := range calls {
		if c.ID == "" {
			return fmt.Errorf("%w: tool %q has an empty tool_use ID", ErrInvalidToolCallID, c.Name)
		}
		if seen[c.ID] {
			return fmt.Errorf("%w: %q", ErrDuplicateToolCallID, c.ID)
		}
		seen[c.ID] = true
	}
	return nil
}

// authorizeBatch resolves and authorizes every call serially in call order.
// Resolving and authorizing stay serial so Policy ordering is deterministic and
// the batch fails fast on the first missing or denied tool. Suspends do not
// short-circuit: the loop continues so a later deny still takes precedence over
// an earlier suspend. It returns the selected tools (indexed by call order) and
// the suspended calls collected in call order. When the returned pending slice
// is non-empty, the batch suspended and the caller must not execute it.
func (r *Runner) authorizeBatch(ctx context.Context, st *runState, toolsByName map[string]tool.Tool, calls []message.ToolUse) ([]tool.Tool, []PendingToolCall, error) {
	if err := validateToolCallIDs(calls); err != nil {
		return nil, nil, err
	}
	selected := make([]tool.Tool, len(calls))
	var pending []PendingToolCall
	for i, call := range calls {
		t, ok := toolsByName[call.Name]
		if !ok {
			return nil, nil, fmt.Errorf("%w: %q", ErrToolNotFound, call.Name)
		}
		decision, err := r.authorize(ctx, st, t, call)
		if err != nil {
			return nil, nil, err
		}
		if decision.ResolvedKind() == policy.DecisionSuspend {
			pending = append(pending, PendingToolCall{Tool: t.Info(), Call: call, Reason: decision.Reason})
			continue
		}
		selected[i] = t
	}
	return selected, pending, nil
}

// errSiblingFailed is the cancellation cause used to stop sibling tools after
// the first failure in a parallel batch. It lets the runner tell an
// internally-induced cancellation apart from a genuine tool error or an
// external (parent ctx) cancellation.
var errSiblingFailed = errors.New("runner: sibling tool failed")

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

// toolOutcome is the result of one parallel tool execution, kept at its call
// index so results stay in call order regardless of completion order.
// inducedCancel marks a context.Canceled that was caused by the batch's own
// fail-fast cancellation (not a genuine tool error or external cancellation);
// such outcomes are skipped when selecting the first error so the returned
// error matches the serial path (loop-semantics I6).
type toolOutcome struct {
	out           tool.Result
	err           error
	inducedCancel bool
}

// finishToolCallsParallel executes an already-authorized, ParallelSafe batch
// concurrently (bounded by maxConcurrency), appends one RoleTool message with
// results in call order, and applies handoffs in call order. The first error by
// call order cancels siblings and is returned.
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

// firstBatchError returns the batch's terminating error matching the serial
// path: an external (parent ctx) cancellation outranks everything; otherwise it
// is the first error by call order that is not an internally-induced
// cancellation. Internally-induced cancellations are skipped because the serial
// path never cancels lower-index siblings.
func (r *Runner) firstBatchError(parent context.Context, outcomes []toolOutcome) error {
	if err := parent.Err(); err != nil {
		return err
	}
	for _, oc := range outcomes {
		if oc.err != nil && !oc.inducedCancel {
			return oc.err
		}
	}
	return nil
}

// executeParallel runs every selected tool in its own goroutine, bounded by a
// maxConcurrency-sized semaphore. The first error cancels a derived context
// (with cause errSiblingFailed) so ctx-aware siblings stop early; those induced
// cancellations are tagged so they do not shadow the genuine first error.
// Results are written back at their call index.
func (r *Runner) executeParallel(ctx context.Context, st *runState, selected []tool.Tool, calls []message.ToolUse) []toolOutcome {
	parent := ctx
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
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
				outcomes[i] = toolOutcome{err: err, inducedCancel: isInducedCancel(parent, ctx, err)}
				return
			}
			_, out, err := r.execute(ctx, st, selected[i], calls[i])
			outcomes[i] = toolOutcome{out: out, err: err, inducedCancel: isInducedCancel(parent, ctx, err)}
			if err != nil {
				cancel(errSiblingFailed)
			}
		}(i)
	}
	wg.Wait()
	return outcomes
}

// isInducedCancel reports whether err is a context.Canceled produced by the
// batch's own fail-fast cancellation (cause errSiblingFailed) while the parent
// context is still live. A tool that returns context.Canceled on its own (no
// sibling has cancelled the batch yet) is treated as a genuine error.
func isInducedCancel(parent, batch context.Context, err error) bool {
	return err != nil &&
		errors.Is(err, context.Canceled) &&
		parent.Err() == nil &&
		context.Cause(batch) == errSiblingFailed
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
