package anthropic

import (
	"context"
	"encoding/json"
	"iter"
	"strings"

	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/providers/internal/sse"
	"github.com/nethinwei/fino/tool"
)

type streamEvent struct {
	Type         string `json:"type"`
	Index        int    `json:"index"`
	ContentBlock struct {
		Type string `json:"type"`
		ID   string `json:"id"`
		Name string `json:"name"`
		Text string `json:"text"`
	} `json:"content_block"`
	Delta struct {
		Type        string `json:"type"`
		Text        string `json:"text"`
		Thinking    string `json:"thinking"`
		PartialJSON string `json:"partial_json"`
	} `json:"delta"`
}

// Stream consumes the Anthropic/DeepSeek SSE response, yielding a
// ContentBlockDelta (thinking) per reasoning fragment, a TextDelta per text
// fragment, and a single TurnMessage assembled from accumulated content
// blocks. Terminal errors are yielded as model.StreamError alongside a non-nil
// iterator error.
func (m *Model) Stream(ctx context.Context, messages []message.Message, tools []tool.Info, opts ...model.Option) iter.Seq2[model.Event, error] {
	return func(yield func(model.Event, error) bool) {
		resp, err := m.post(ctx, m.buildRequest(messages, tools, opts, true))
		if err != nil {
			yield(model.StreamError{Err: err}, err)
			return
		}
		defer resp.Body.Close()
		acc := &accumulator{idx: map[int]*accBlock{}}
		final := func() model.Event { return model.TurnMessage{Message: acc.finalMessage()} }
		sse.Stream(resp.Body, acc.handle, final)(yield)
	}
}

// handle unmarshals one stream event and folds it into the accumulator,
// returning any deltas (text or thinking) as events to emit.
func (a *accumulator) handle(payload string) ([]model.Event, bool, error) {
	var ev streamEvent
	if err := json.Unmarshal([]byte(payload), &ev); err != nil {
		return nil, false, err
	}
	return a.apply(ev), false, nil
}

// accBlock accumulates one content block across stream events.
type accBlock struct {
	typ      string
	id       string
	name     string
	text     strings.Builder
	thinking strings.Builder
	input    strings.Builder
}

// accumulator assembles streamed content blocks into a final message.
type accumulator struct {
	blocks []*accBlock
	idx    map[int]*accBlock
}

// apply folds one event into the accumulator and returns any deltas it carried
// (text or thinking) as events to emit.
func (a *accumulator) apply(ev streamEvent) []model.Event {
	switch ev.Type {
	case "content_block_start":
		b := &accBlock{typ: ev.ContentBlock.Type, id: ev.ContentBlock.ID, name: ev.ContentBlock.Name}
		a.blocks = append(a.blocks, b)
		a.idx[ev.Index] = b
		// A text block may carry initial text in its start event; surface it as
		// a delta so streaming consumers do not drop it.
		if b.typ == "text" && ev.ContentBlock.Text != "" {
			b.text.WriteString(ev.ContentBlock.Text)
			return []model.Event{model.TextDelta{Text: ev.ContentBlock.Text}}
		}
	case "content_block_delta":
		return a.applyDelta(ev)
	}
	return nil
}

// applyDelta folds a content_block_delta into its block, returning the text or
// thinking delta as an event.
func (a *accumulator) applyDelta(ev streamEvent) []model.Event {
	b := a.idx[ev.Index]
	if b == nil {
		return nil
	}
	switch ev.Delta.Type {
	case "text_delta":
		b.text.WriteString(ev.Delta.Text)
		return []model.Event{model.TextDelta{Text: ev.Delta.Text}}
	case "thinking_delta":
		b.thinking.WriteString(ev.Delta.Thinking)
		return []model.Event{model.ContentBlockDelta{Index: ev.Index, Block: message.NewThinking(ev.Delta.Thinking)}}
	case "input_json_delta":
		b.input.WriteString(ev.Delta.PartialJSON)
	}
	return nil
}

// finalMessage builds the assistant message from accumulated blocks in order.
func (a *accumulator) finalMessage() message.Message {
	blocks := make([]message.Block, 0, len(a.blocks))
	for _, b := range a.blocks {
		switch b.typ {
		case "text":
			blocks = append(blocks, message.NewText(b.text.String()))
		case "thinking":
			blocks = append(blocks, message.NewThinking(b.thinking.String()))
		case "tool_use":
			input := b.input.String()
			if input == "" {
				input = "{}"
			}
			blocks = append(blocks, message.NewToolUse(b.id, b.name, json.RawMessage(input)))
		}
	}
	return message.Assistant(blocks...)
}
