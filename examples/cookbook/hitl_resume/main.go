// Command hitl_resume shows human-in-the-loop tool approval built only from
// fino primitives: a Policy gates a sensitive tool, and after the human
// approves, the run continues mid-batch via runner.WithResumeFromPendingTools.
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
	"github.com/nethinwei/fino/hooks"
	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/policy"
	"github.com/nethinwei/fino/runner"
	"github.com/nethinwei/fino/tool"
)

// scriptedModel returns one assistant message per turn. It is the whole "LLM"
// for this example: turn 0 requests the delete_file tool, turn 1 wraps up.
type scriptedModel struct {
	turns []message.Message
	i     int
}

func (m *scriptedModel) next() message.Message {
	if m.i >= len(m.turns) {
		return message.Assistant(message.NewText("done"))
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

// gatePolicy denies a fixed set of sensitive tools so a human can decide.
type gatePolicy struct{ gated map[string]bool }

func (p gatePolicy) Authorize(_ context.Context, req policy.Request) (policy.Decision, error) {
	if p.gated[req.Tool.Name] {
		return policy.Decision{Allow: false, Reason: "needs human approval"}, nil
	}
	return policy.Decision{Allow: true}, nil
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

	// Turn 0: the model asks to delete a file. We stop here for approval.
	toolUse := message.Assistant(message.NewToolUse("c1", "delete_file", json.RawMessage(`{"path":"/tmp/report.txt"}`)))
	m := &scriptedModel{turns: []message.Message{toolUse}}

	// Capture the real assistant message the model produced, instead of
	// hand-rebuilding it. A server would persist exactly this — the user prompt
	// plus the captured assistant tool_use — when the policy pauses the run.
	var capturedAssistant *message.Message
	capture := runner.WithHooks(&hooks.Hooks{
		AfterModel: func(_ context.Context, res hooks.ModelResult) {
			if len(res.Message.ToolUses()) > 0 {
				snapshot := *res.Message
				capturedAssistant = &snapshot
			}
		},
	})
	gated := runner.WithPolicy(gatePolicy{gated: map[string]bool{"delete_file": true}})
	r, err := runner.New(m, gated, capture)
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()

	const prompt = "Delete /tmp/report.txt"
	// First leg: the policy denies the gated call. The AfterModel hook has
	// already captured the dangling assistant tool_use for us.
	_, err = r.Run(ctx, a, runner.Text(prompt))
	if err == nil {
		log.Fatal("expected the gated tool to be denied")
	}
	if capturedAssistant == nil {
		log.Fatal("expected to capture the assistant tool_use before denial")
	}
	fmt.Printf("paused for approval: %v\n", err)

	// Persist what a real app would store while waiting for a human.
	pending := []message.Message{
		message.UserText(prompt),
		*capturedAssistant, // dangling assistant tool_use, no result yet
	}

	// ----- a human reviews and approves here -----

	// Second leg: a runner whose policy now allows the tool, resumed from the
	// pending tool_use. No checkpoint type, no graph — just history + a seam.
	m2 := &scriptedModel{}          // after the tool runs, the model wraps up
	approved, err := runner.New(m2) // default AllowAll policy
	if err != nil {
		log.Fatal(err)
	}
	res, err := approved.Run(ctx, a, runner.Messages(pending), runner.WithResumeFromPendingTools())
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
