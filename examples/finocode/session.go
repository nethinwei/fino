package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/runner"
)

// session holds the conversation state across turns. Each turn starts from the
// main agent with the user-selected mode; a within-turn handoff to a subagent
// is transient, so control returns to the main agent on the next turn.
type session struct {
	reg       *agentRegistry
	runner    *runner.Runner
	history   []message.Message
	mode      string
	modelOpts []model.Option
}

func newSession(reg *agentRegistry, r *runner.Runner) *session {
	return &session{
		reg:       reg,
		runner:    r,
		mode:      "code",
		modelOpts: []model.Option{model.WithMaxTokens(1024), model.WithTemperature(0.3)},
	}
}

// runTurn streams one user turn, rendering events live and reconstructing the
// turn's messages so the conversation history carries forward.
func (s *session) runTurn(ctx context.Context, userText string) {
	base := append(s.clone(), message.UserText(userText))
	rnd := &renderer{}
	hb := &historyBuilder{}
	var streamErr error
	for ev, err := range s.runner.Stream(ctx, s.reg.main, runner.Messages(base),
		runner.WithMode(s.mode), runner.WithModelOptions(s.modelOpts...)) {
		if err != nil {
			streamErr = err
			break
		}
		hb.feed(ev)
		rnd.render(ev)
	}
	rnd.finish()
	if streamErr != nil {
		if errors.Is(streamErr, context.Canceled) {
			fmt.Println(dim("  (interrupted)"))
		}
		return // discard the partial turn; the OnError hook reported terminal errors
	}
	s.history = append(base, hb.messages...)
}

func (s *session) clone() []message.Message {
	return append([]message.Message(nil), s.history...)
}

// cycleMode advances the main agent to its next mode, wrapping around. It backs
// the Shift+Tab keybinding and the /mode command's bare form.
func (s *session) cycleMode() {
	modes := s.reg.modes[s.reg.main.Name()]
	if len(modes) == 0 {
		return
	}
	for i, m := range modes {
		if m == s.mode {
			s.mode = modes[(i+1)%len(modes)]
			return
		}
	}
	s.mode = modes[0]
}

// historyBuilder reassembles the assistant and tool messages of a streamed run
// from the event sequence (intermediate assistant messages are not forwarded
// by the Runner, so they are rebuilt from TextDelta and ToolCall events).
type historyBuilder struct {
	messages []message.Message
	text     strings.Builder
	calls    []message.ToolUse
	results  []message.Block
}

func (h *historyBuilder) feed(ev model.Event) {
	switch e := ev.(type) {
	case model.TextDelta:
		if len(h.results) > 0 {
			h.flush()
		}
		h.text.WriteString(e.Text)
	case model.ToolCall:
		if len(h.results) > 0 {
			h.flush()
		}
		h.calls = append(h.calls, e.Call)
	case model.ToolResult:
		h.results = append(h.results, message.NewToolResult(e.CallID, e.Name, e.Result.Content, e.Result.IsError))
	case model.FinalMessage:
		if len(h.results) > 0 {
			h.flush()
		}
		h.messages = append(h.messages, e.Message)
		h.reset()
	}
}

// flush commits one intermediate turn: the assistant message (text plus tool
// calls) followed by the batched tool results.
func (h *historyBuilder) flush() {
	blocks := make([]message.Block, 0, 1+len(h.calls))
	if h.text.Len() > 0 {
		blocks = append(blocks, message.NewText(h.text.String()))
	}
	for _, c := range h.calls {
		blocks = append(blocks, message.NewToolUse(c.ID, c.Name, c.Input))
	}
	if len(blocks) > 0 {
		h.messages = append(h.messages, message.Assistant(blocks...))
	}
	if len(h.results) > 0 {
		h.messages = append(h.messages, message.ToolResults(h.orderedResults()...))
	}
	h.reset()
}

// orderedResults returns the tool results in tool_use (call) order. Parallel
// execution emits results in completion order, so this realigns them with the
// assistant's tool_use blocks by ID; unmatched results keep arrival order.
func (h *historyBuilder) orderedResults() []message.Block {
	if len(h.results) <= 1 {
		return h.results
	}
	byID := make(map[string]message.Block, len(h.results))
	for _, b := range h.results {
		byID[b.ToolUseID] = b
	}
	ordered := make([]message.Block, 0, len(h.results))
	for _, c := range h.calls {
		if b, ok := byID[c.ID]; ok {
			ordered = append(ordered, b)
			delete(byID, c.ID)
		}
	}
	for _, b := range h.results {
		if _, ok := byID[b.ToolUseID]; ok {
			ordered = append(ordered, b)
			delete(byID, b.ToolUseID)
		}
	}
	return ordered
}

func (h *historyBuilder) reset() {
	h.text.Reset()
	h.calls = nil
	h.results = nil
}
