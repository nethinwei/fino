package runner

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/tool"
)

var (
	// ErrNotSuspended is returned by Result.SuspendedRun when the result did not
	// suspend (it completed normally). It is distinct from ErrInvalidApproval.
	ErrNotSuspended = errors.New("result is not suspended")
	// ErrInvalidApproval is the sentinel wrapped by ApprovalError. It reports an
	// invalid resume input: a malformed SuspendedRun, or an approval set that
	// does not match the suspended pending calls (missing, unknown, duplicate).
	ErrInvalidApproval = errors.New("invalid approval")
	// ErrResumeAgentMismatch indicates ResumeApproved was called with an agent
	// other than the one active when the run suspended (SuspendedRun.LastAgentName).
	// Resuming under the wrong agent would resolve tools in the wrong context.
	ErrResumeAgentMismatch = errors.New("resume agent does not match suspended agent")
)

// SuspendedRun is a plain value snapshot of a suspended run, extracted from a
// suspended Result via Result.SuspendedRun. It is not a checkpoint, state
// machine, or anything the core persists; the caller persists it however they
// like and later passes it to Runner.ResumeApproved together with the live
// agent. LastAgentName and LastMode record the agent and mode active at suspend
// time (a handoff may have switched away from the root agent before the
// suspend); ResumeApproved checks the agent the caller passes against
// LastAgentName so the resume re-enters the correct context. The agent itself is
// code reconstructed by the caller, so the snapshot holds only its name — no
// live object reference. PendingCalls holds only the suspended calls.
//
// The snapshot contains no live object references, so it is straightforward to
// serialize; whether json.Marshal succeeds depends on the caller's data —
// notably tool.Info.Metadata (map[string]any) on each PendingCall, which the
// core does not sanitize. Keep tool metadata JSON-marshalable if you persist
// SuspendedRun as JSON.
type SuspendedRun struct {
	Messages      []message.Message
	LastAgentName string
	LastMode      string
	PendingCalls  []PendingToolCall
	// RunID is the run-scoped identifier captured at suspend time (empty when
	// the original run used no WithRunID). ResumeApproved restores it so
	// approved and previously-allowed calls receive the same
	// tool.ExecutionContext.IdempotencyKey they would have in the original run
	// (loop-semantics I13). Fixtures without it deserialize to "".
	RunID string
}

// Approval is a human decision about one suspended tool call, matched to the
// call by CallID (the tool_use block's ID). Approved=true executes the tool;
// Approved=false rejects it and writes Reason into a model-visible error
// tool_result so the model can adjust.
type Approval struct {
	CallID   string
	Approved bool
	Reason   string
}

// ApprovalError reports that the approvals passed to ResumeApproved did not
// validly cover the suspended pending calls. It wraps ErrInvalidApproval.
type ApprovalError struct {
	Missing   []string // pending CallIDs with no approval
	Unknown   []string // approval CallIDs not in the pending set
	Duplicate []string // CallIDs approved more than once
}

// Error implements the error interface.
func (e *ApprovalError) Error() string {
	return fmt.Sprintf("invalid approvals: missing=%v unknown=%v duplicate=%v",
		e.Missing, e.Unknown, e.Duplicate)
}

// Unwrap returns ErrInvalidApproval so errors.Is(err, ErrInvalidApproval) is true.
func (e *ApprovalError) Unwrap() error { return ErrInvalidApproval }

// SuspendedRun extracts a value snapshot from a suspended Result. It returns
// ErrNotSuspended if the result completed normally.
func (r *Result) SuspendedRun() (SuspendedRun, error) {
	if !r.Suspended {
		return SuspendedRun{}, ErrNotSuspended
	}
	name := ""
	if r.LastAgent != nil {
		name = r.LastAgent.Name()
	}
	return SuspendedRun{
		Messages:      r.Messages,
		LastAgentName: name,
		LastMode:      r.LastMode,
		PendingCalls:  r.PendingCalls,
		RunID:         r.runID,
	}, nil
}

// ResumeApproved continues a suspended run after a human has approved or
// rejected its pending tool calls. It validates that a matches the agent active
// at suspend time and that the approvals validly cover the pending calls, then
// executes the batch in call order — approved (and previously-allowed) calls run
// their tools; rejected calls produce a model-visible error tool_result — and
// resumes the ReAct loop from the next model turn. It does not re-consult the
// Policy: human approval replaces policy authorization for the suspended calls.
func (r *Runner) ResumeApproved(ctx context.Context, a *agent.Agent, suspended SuspendedRun, approvals []Approval, opts ...RunOption) (*Result, error) {
	if a == nil {
		return nil, errors.New("agent is required")
	}
	if a.Name() != suspended.LastAgentName {
		return nil, fmt.Errorf("%w: passed %q, want %q", ErrResumeAgentMismatch, a.Name(), suspended.LastAgentName)
	}
	if err := validateSuspendedRun(suspended, approvals); err != nil {
		return nil, err
	}
	mode, ok := a.Mode(suspended.LastMode)
	if !ok {
		return nil, fmt.Errorf("mode %q not found", suspended.LastMode)
	}
	cfg := runConfig{modeName: suspended.LastMode}
	for _, opt := range opts {
		if opt != nil {
			opt(&cfg)
		}
	}
	// The resumed batch's IdempotencyKey must match the original run, so the
	// suspended RunID always wins over any WithRunID passed on resume
	// (loop-semantics I13).
	cfg.runID = suspended.RunID
	// Mode cannot be overridden on resume: the suspended batch's tool_uses were
	// resolved against suspended.LastMode, so resuming in another mode would
	// resolve them in the wrong tool set. Other run options (e.g. model options)
	// are honored.
	if cfg.modeName != suspended.LastMode {
		return nil, fmt.Errorf("%w: cannot override mode on resume (got %q, suspended in %q)",
			ErrInvalidApproval, cfg.modeName, suspended.LastMode)
	}
	st := &runState{
		agent:   a,
		mode:    mode,
		history: append([]message.Message(nil), suspended.Messages...),
		cfg:     cfg,
	}
	ctx, err := r.resumeExecuteBatch(ctx, st, suspended.PendingCalls, approvals)
	if err != nil {
		return nil, err
	}
	return r.loop(ctx, st)
}

// validateSuspendedRun checks the snapshot is well-formed and the approvals
// exactly cover the suspended pending calls. All failures wrap ErrInvalidApproval
// (as an ApprovalError when the mismatch is about the approval set), so callers
// can branch on errors.Is(err, ErrInvalidApproval). It never executes a tool.
func validateSuspendedRun(suspended SuspendedRun, approvals []Approval) error {
	if message.HasSystem(suspended.Messages) {
		return ErrSystemMessageInHistory
	}
	batch := pendingToolUses(suspended.Messages)
	if len(batch) == 0 {
		return fmt.Errorf("%w: suspended history has no dangling tool_use", ErrInvalidApproval)
	}
	if len(suspended.PendingCalls) == 0 {
		// A genuine suspended Result always has ≥1 suspended call. An empty
		// pending set with dangling tool_uses would silently degrade into a
		// blind, approval-free resume, so reject it.
		return fmt.Errorf("%w: suspended run has no pending calls to approve", ErrInvalidApproval)
	}
	inBatch := make(map[string]bool, len(batch))
	for _, tu := range batch {
		inBatch[tu.ID] = true
	}
	pendingIDs := make(map[string]bool, len(suspended.PendingCalls))
	for _, pc := range suspended.PendingCalls {
		if !inBatch[pc.Call.ID] {
			return fmt.Errorf("%w: pending call %q not in suspended batch", ErrInvalidApproval, pc.Call.ID)
		}
		if pendingIDs[pc.Call.ID] {
			return fmt.Errorf("%w: duplicate pending call %q", ErrInvalidApproval, pc.Call.ID)
		}
		pendingIDs[pc.Call.ID] = true
	}

	counts := make(map[string]int, len(approvals))
	var unknown []string
	for _, ap := range approvals {
		counts[ap.CallID]++
		if !pendingIDs[ap.CallID] {
			unknown = append(unknown, ap.CallID)
		}
	}
	var missing, duplicate []string
	for id := range pendingIDs {
		if counts[id] == 0 {
			missing = append(missing, id)
		}
	}
	for id, n := range counts {
		if n > 1 && pendingIDs[id] {
			duplicate = append(duplicate, id)
		}
	}
	if len(missing)+len(unknown)+len(duplicate) > 0 {
		sort.Strings(missing)
		sort.Strings(unknown)
		sort.Strings(duplicate)
		return &ApprovalError{Missing: missing, Unknown: unknown, Duplicate: duplicate}
	}
	return nil
}

// resumeExecuteBatch executes the suspended batch in call order without
// re-authorizing: pending calls run their tool when approved, or produce an
// error tool_result when rejected; previously-allowed calls (not suspended) run
// directly. It applies handoffs batch-terminal in call order and appends one
// RoleTool message, mirroring the normal serial batch path.
func (r *Runner) resumeExecuteBatch(ctx context.Context, st *runState, pendingCalls []PendingToolCall, approvals []Approval) (context.Context, error) {
	_, toolsByName := collectTools(st.mode.Tools)
	pendingSet := make(map[string]bool, len(pendingCalls))
	for _, pc := range pendingCalls {
		pendingSet[pc.Call.ID] = true
	}
	approvalByID := make(map[string]Approval, len(approvals))
	for _, ap := range approvals {
		approvalByID[ap.CallID] = ap
	}

	calls := pendingToolUses(st.history)
	blocks := make([]message.Block, 0, len(calls))
	selected := make([]tool.Tool, 0, len(calls))
	for _, call := range calls {
		if err := ctx.Err(); err != nil {
			r.onError(ctx, err)
			return ctx, err
		}
		if pendingSet[call.ID] && !approvalByID[call.ID].Approved {
			reason := approvalByID[call.ID].Reason
			blocks = append(blocks, message.NewToolResult(call.ID, call.Name,
				[]message.Block{message.NewText("rejected: " + reason)}, true))
			selected = append(selected, nil)
			continue
		}
		t, ok := toolsByName[call.Name]
		if !ok {
			err := fmt.Errorf("%w: %q", ErrToolNotFound, call.Name)
			r.onError(ctx, err)
			return ctx, err
		}
		newCtx, out, err := r.execute(ctx, st, t, call)
		if err != nil {
			r.onError(ctx, err)
			return ctx, err
		}
		ctx = newCtx
		blocks = append(blocks, message.NewToolResult(call.ID, call.Name, out.Content, out.IsError))
		selected = append(selected, t)
	}
	if err := st.applyHandoffs(selected); err != nil {
		return ctx, err
	}
	st.history = append(st.history, message.ToolResults(blocks...))
	return ctx, nil
}
