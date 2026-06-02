// Command hitl_resume shows human-in-the-loop tool approval built only from
// fino primitives: a Policy suspends a sensitive tool (DecisionSuspend), the run
// halts with a suspended Result, a human approves or rejects each pending call,
// and runner.ResumeApproved continues the ReAct loop.
//
// It uses a tiny scripted model so it runs offline and deterministically — no
// API key required. The same wiring works against any real provider.
//
//	go run ./examples/cookbook/hitl_resume
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"log"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/policy"
	"github.com/nethinwei/fino/runner"
	"github.com/nethinwei/fino/tool"
)

// scriptedModel returns one assistant message per turn. It is the whole "LLM"
// for this example: turn 0 requests the delete_file tool, later turns wrap up.
type scriptedModel struct {
	turns []message.Message
	i     int
}

func (m *scriptedModel) next() message.Message {
	if m.i >= len(m.turns) {
		return message.Assistant(message.NewText("done — file deleted"))
	}
	msg := m.turns[m.i]
	m.i++
	return msg
}

func (m *scriptedModel) Generate(context.Context, []message.Message, []tool.Info, ...model.Option) (*message.Message, error) {
	msg := m.next()
	return &msg, nil
}

func (m *scriptedModel) Stream(context.Context, []message.Message, []tool.Info, ...model.Option) iter.Seq2[model.Event, error] {
	return func(yield func(model.Event, error) bool) {
		msg := m.next()
		yield(model.TurnMessage{Message: msg}, nil)
	}
}

// gatePolicy suspends a fixed set of sensitive tools so a human can decide.
type gatePolicy struct{ gated map[string]bool }

func (p gatePolicy) Authorize(_ context.Context, req policy.Request) (policy.Decision, error) {
	if p.gated[req.Tool.Name] {
		return policy.Decision{Kind: policy.DecisionSuspend, Reason: "needs human approval"}, nil
	}
	return policy.Decision{Kind: policy.DecisionAllow}, nil
}

type pathInput struct {
	Path string `json:"path" jsonschema:"description=file to delete"`
}

func main() {
	deleteFile, err := tool.NewFunc("delete_file", "Delete a file",
		func(_ context.Context, in pathInput) (string, error) {
			return "deleted " + in.Path, nil
		})
	if err != nil {
		log.Fatal(err)
	}
	mode, err := agent.NewMode("default", "Delete files the user asks about.", agent.WithTools(deleteFile))
	if err != nil {
		log.Fatal(err)
	}
	a, err := agent.New("assistant", agent.WithMode(mode), agent.WithDefaultMode("default"))
	if err != nil {
		log.Fatal(err)
	}

	// Turn 0: the model asks to delete a file. The policy suspends it.
	toolUse := message.Assistant(message.NewToolUse("c1", "delete_file", json.RawMessage(`{"path":"/tmp/report.txt"}`)))
	m := &scriptedModel{turns: []message.Message{toolUse}}

	gated := runner.WithPolicy(gatePolicy{gated: map[string]bool{"delete_file": true}})
	r, err := runner.New(m, gated)
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()

	// First leg: the run suspends before executing the gated tool. The history
	// already holds the dangling assistant tool_use — nothing to hand-capture.
	res, err := r.Run(ctx, a, runner.Text("Delete /tmp/report.txt"))
	if err != nil {
		log.Fatal(err)
	}
	if !res.Suspended {
		log.Fatal("expected the gated tool to suspend the run")
	}

	// SuspendedRun is a plain, serializable value snapshot. A server can JSON it
	// while waiting for a human; here we keep it in memory. LastAgentName records
	// the agent active at suspend time so the resume re-enters the right context
	// — the live agent is reconstructed by the caller (here, the same `a`).
	suspended, err := res.SuspendedRun()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("paused for approval (%d pending):\n", len(suspended.PendingCalls))
	for _, pc := range suspended.PendingCalls {
		fmt.Printf("  - %s%s  reason=%q\n", pc.Call.Name, pc.Call.Input, pc.Reason)
	}

	// ----- a human reviews and decides here -----
	approvals := make([]runner.Approval, 0, len(suspended.PendingCalls))
	for _, pc := range suspended.PendingCalls {
		approvals = append(approvals, runner.Approval{CallID: pc.Call.ID, Approved: true})
	}

	// Second leg: resume with the human's decisions. Approved calls run their
	// real tool; rejected calls become model-visible error results. No
	// checkpoint type, no graph — just the snapshot plus the approvals.
	res, err = r.ResumeApproved(ctx, a, suspended, approvals)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("resumed and finished: %q\n", res.Text())
	for _, msg := range res.Messages {
		if msg.Role == message.RoleTool && len(msg.Content) > 0 && len(msg.Content[0].Content) > 0 {
			fmt.Printf("tool result: %s\n", msg.Content[0].Content[0].Text)
		}
	}
}
