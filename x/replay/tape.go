package replay

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/policy"
	"github.com/nethinwei/fino/runner"
)

// EventKind names a tape event. The tape records the public execution decisions
// that make a run reproducible and auditable: model responses, policy
// decisions, tool executions, suspensions, approvals, resumes, and termination.
type EventKind string

const (
	EventModelResponse  EventKind = "model_response"
	EventPolicyDecision EventKind = "policy_decision"
	EventToolExecution  EventKind = "tool_execution"
	EventSuspend        EventKind = "suspend"
	EventApproval       EventKind = "approval"
	EventResume         EventKind = "resume"
	EventTermination    EventKind = "termination"
)

// Status values are strings to keep the tape JSON stable and simple. They
// classify a public run outcome, not a business result.
const (
	StatusCompleted = "completed"
	StatusSuspended = "suspended"
	StatusError     = "error"
)

// Event is one entry on the execution tape. Kind selects which payload field is
// populated; the others are nil. The nested payload shape is only for audit
// fixtures: it is not a provider wire format and does not change the flat
// message.Block discriminated union used for model messages.
type Event struct {
	Kind EventKind `json:"kind"`

	ModelResponse  *ModelResponseEvent  `json:"model_response,omitempty"`
	PolicyDecision *PolicyDecisionEvent `json:"policy_decision,omitempty"`
	ToolExecution  *ToolExecutionEvent  `json:"tool_execution,omitempty"`
	Suspend        *SuspendEvent        `json:"suspend,omitempty"`
	Approval       *ApprovalEvent       `json:"approval,omitempty"`
	Resume         *ResumeEvent         `json:"resume,omitempty"`
	Termination    *TerminationEvent    `json:"termination,omitempty"`
}

// ModelResponseEvent records one model response.
type ModelResponseEvent struct {
	Message message.Message `json:"message"`
}

// PolicyDecisionEvent records one authorization check: the request, the
// decision, and the policy system error (if any, distinct from a deny).
type PolicyDecisionEvent struct {
	Request  policy.Request  `json:"request"`
	Decision policy.Decision `json:"decision"`
	Err      string          `json:"err,omitempty"`
}

// ToolExecutionEvent records one tool execution.
type ToolExecutionEvent struct {
	Record ToolRecord `json:"record"`
}

// SuspendEvent records the suspended run snapshot at a suspend boundary.
type SuspendEvent struct {
	Suspended runner.SuspendedRun `json:"suspended"`
}

// ApprovalEvent records the human approvals supplied before a resume.
type ApprovalEvent struct {
	Approvals []runner.Approval `json:"approvals"`
}

// ResumeEvent records the result of a ResumeApproved boundary call. It is
// appended after ResumeApproved returns, so model/tool events produced during
// the resumed loop naturally appear before it.
type ResumeEvent struct {
	LastAgentName string            `json:"last_agent_name"`
	LastMode      string            `json:"last_mode"`
	Approvals     []runner.Approval `json:"approvals"`
	Status        string            `json:"status"`
	Err           string            `json:"err,omitempty"`
}

// TerminationEvent records a public run outcome. FinalText is set only for a
// completed run.
type TerminationEvent struct {
	Status    string `json:"status"`
	Err       string `json:"err,omitempty"`
	FinalText string `json:"final_text,omitempty"`
}

func (l *Log) recordEvent(e Event) {
	l.mu.Lock()
	l.Events = append(l.Events, e)
	l.mu.Unlock()
}

// RecordingPolicy wraps a Policy, recording every decision into Log while
// forwarding Authorize to Next unchanged (including policy errors).
type RecordingPolicy struct {
	Next policy.Policy
	Log  *Log
}

// Authorize forwards to Next and records the decision.
func (p RecordingPolicy) Authorize(ctx context.Context, req policy.Request) (policy.Decision, error) {
	decision, err := p.Next.Authorize(ctx, req)
	ev := &PolicyDecisionEvent{Request: req, Decision: decision}
	if err != nil {
		ev.Err = err.Error()
	}
	p.Log.recordEvent(Event{Kind: EventPolicyDecision, PolicyDecision: ev})
	return decision, err
}

// ReplayPolicy serves recorded policy decisions in order and consults no real
// policy system (no human approval state, clock, RBAC, allowlist, or risk
// scoring). Use a pointer; it advances an internal cursor over the tape.
type ReplayPolicy struct {
	Log *Log
	mu  sync.Mutex
	i   int
}

// Authorize returns the next recorded policy decision after validating that the
// current request matches the recorded one on stable execution identity
// (AgentName, ModeName, Tool.Name, raw Input). A replay: error means the
// fixture does not match the current run wiring, not a policy deny. If the
// recorded event carries Err, the recorded decision is returned with that error.
func (p *ReplayPolicy) Authorize(_ context.Context, req policy.Request) (policy.Decision, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	ev, err := p.nextDecision()
	if err != nil {
		return policy.Decision{}, err
	}
	if !sameRequestIdentity(ev.Request, req) {
		return policy.Decision{}, fmt.Errorf("replay: policy request mismatch: recorded %s/%s/%s input %s, got %s/%s/%s input %s",
			ev.Request.AgentName, ev.Request.ModeName, ev.Request.Tool.Name, ev.Request.Input,
			req.AgentName, req.ModeName, req.Tool.Name, req.Input)
	}
	if ev.Err != "" {
		return ev.Decision, errors.New(ev.Err)
	}
	return ev.Decision, nil
}

// nextDecision advances the cursor to the next policy_decision event, skipping
// interleaved model/tool/boundary events (those are replayed from Log.Model and
// Log.Tools, or are non-executable audit data). A policy_decision event with a
// nil payload is a corrupt fixture; it returns a replay: error rather than
// skipping it, so a later decision is not consumed in its place. It returns a
// replay: error when the tape has no more recorded policy decisions.
func (p *ReplayPolicy) nextDecision() (*PolicyDecisionEvent, error) {
	for p.i < len(p.Log.Events) {
		ev := p.Log.Events[p.i]
		p.i++
		if ev.Kind != EventPolicyDecision {
			continue
		}
		if ev.PolicyDecision == nil {
			return nil, errors.New("replay: malformed policy decision event")
		}
		return ev.PolicyDecision, nil
	}
	return nil, errors.New("replay: no more recorded policy decisions")
}

// sameRequestIdentity compares the stable replay key. It deliberately does not
// deep-compare tool.Info.Metadata, which can hold arbitrary user JSON and is
// not part of the replay identity.
func sameRequestIdentity(a, b policy.Request) bool {
	return a.AgentName == b.AgentName &&
		a.ModeName == b.ModeName &&
		a.Tool.Name == b.Tool.Name &&
		string(a.Input) == string(b.Input)
}

// classifyOutcome maps a public run outcome to a Status string. An error
// outranks suspension; otherwise a suspended result is suspended and anything
// else is completed.
func classifyOutcome(res *runner.Result, err error) string {
	switch {
	case err != nil:
		return StatusError
	case res != nil && res.Suspended:
		return StatusSuspended
	default:
		return StatusCompleted
	}
}

// copySuspended copies the top-level slices of a SuspendedRun so a later
// replacement of a caller-held slice element (e.g. messages[0] = ...) cannot
// rewrite the recorded tape. It does not deep-copy nested user-owned data
// (message.Message.Content, message.Block content, json.RawMessage,
// tool.Info.Metadata), which x/replay never sanitizes; mutating those through a
// retained reference still reaches the tape.
func copySuspended(s runner.SuspendedRun) runner.SuspendedRun {
	s.Messages = append([]message.Message(nil), s.Messages...)
	s.PendingCalls = append([]runner.PendingToolCall(nil), s.PendingCalls...)
	return s
}

// copyApprovals copies the approval slice for the same reason as copySuspended.
func copyApprovals(approvals []runner.Approval) []runner.Approval {
	return append([]runner.Approval(nil), approvals...)
}

// RecordSuspend appends a suspend event from a SuspendedRun snapshot. The caller
// records it explicitly after Result.SuspendedRun, since suspension is not
// observable through model.Model, tool.Tool, or policy.Policy. The snapshot's
// top-level slices are copied so later replacement of a caller-held element
// cannot rewrite the tape (nested user-owned data is not deep-copied).
func RecordSuspend(log *Log, suspended runner.SuspendedRun) {
	log.recordEvent(Event{Kind: EventSuspend, Suspend: &SuspendEvent{Suspended: copySuspended(suspended)}})
}

// RecordApproval appends an approval event capturing the human decisions
// supplied for a resume. The approvals slice is copied so later replacement of a
// caller-held element cannot rewrite the tape.
func RecordApproval(log *Log, approvals []runner.Approval) {
	log.recordEvent(Event{Kind: EventApproval, Approval: &ApprovalEvent{Approvals: copyApprovals(approvals)}})
}

// RecordResume appends a resume boundary event after Runner.ResumeApproved
// returns. It records the suspended agent/mode, the approvals used, and the
// resume outcome (status plus error message, if any). The approvals slice is
// copied so later replacement of a caller-held element cannot rewrite the tape.
func RecordResume(log *Log, suspended runner.SuspendedRun, approvals []runner.Approval, res *runner.Result, err error) {
	ev := &ResumeEvent{
		LastAgentName: suspended.LastAgentName,
		LastMode:      suspended.LastMode,
		Approvals:     copyApprovals(approvals),
		Status:        classifyOutcome(res, err),
	}
	if err != nil {
		ev.Err = err.Error()
	}
	log.recordEvent(Event{Kind: EventResume, Resume: ev})
}

// RecordTermination appends a termination event classifying a public run
// outcome. It does not call hooks, wrap errors, or affect control flow.
func RecordTermination(log *Log, res *runner.Result, err error) {
	ev := &TerminationEvent{Status: classifyOutcome(res, err)}
	if err != nil {
		ev.Err = err.Error()
	} else if res != nil && !res.Suspended {
		ev.FinalText = res.Text()
	}
	log.recordEvent(Event{Kind: EventTermination, Termination: ev})
}
