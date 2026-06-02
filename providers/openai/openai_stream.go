package openai

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

type streamChunk struct {
	Choices []struct {
		Delta struct {
			Content          string           `json:"content"`
			ReasoningContent string           `json:"reasoning_content"`
			ToolCalls        []streamToolCall `json:"tool_calls"`
		} `json:"delta"`
	} `json:"choices"`
}

type streamToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// Stream consumes the OpenAI/DeepSeek SSE response, yielding a ContentBlockDelta
// (thinking) per reasoning fragment, a TextDelta per content fragment, and a
// single TurnMessage assembled from accumulated reasoning, text, and tool
// calls. Terminal errors are yielded as model.StreamError alongside a non-nil
// iterator error.
func (m *Model) Stream(ctx context.Context, messages []message.Message, tools []tool.Info, opts ...model.Option) iter.Seq2[model.Event, error] {
	return func(yield func(model.Event, error) bool) {
		resp, err := m.post(ctx, m.buildRequest(messages, tools, opts, true))
		if err != nil {
			yield(model.StreamError{Err: err}, err)
			return
		}
		defer resp.Body.Close()
		acc := &accumulator{idx: map[int]int{}}
		final := func() model.Event { return model.TurnMessage{Message: acc.finalMessage()} }
		sse.Stream(resp.Body, acc.handle, final)(yield)
	}
}

// handle folds one chunk into the accumulator and returns its reasoning and
// text deltas as events (reasoning first). The "[DONE]" sentinel ends the
// stream.
func (a *accumulator) handle(payload string) ([]model.Event, bool, error) {
	if payload == "[DONE]" {
		return nil, true, nil
	}
	var chunk streamChunk
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return nil, false, err
	}
	text, reasoning := a.apply(chunk)
	var events []model.Event
	if reasoning != "" {
		events = append(events, model.ContentBlockDelta{Block: message.NewThinking(reasoning)})
	}
	if text != "" {
		events = append(events, model.TextDelta{Text: text})
	}
	return events, false, nil
}

// accCall accumulates one streamed tool call across chunks.
type accCall struct {
	id   string
	name string
	args strings.Builder
}

// accumulator assembles streamed text and tool calls into a final message.
type accumulator struct {
	text      strings.Builder
	reasoning strings.Builder
	calls     []*accCall
	idx       map[int]int
}

// apply folds one chunk into the accumulator and returns the text and
// reasoning deltas it carried, so the caller can stream each separately.
func (a *accumulator) apply(chunk streamChunk) (text, reasoning string) {
	for _, choice := range chunk.Choices {
		text += choice.Delta.Content
		reasoning += choice.Delta.ReasoningContent
		a.text.WriteString(choice.Delta.Content)
		a.reasoning.WriteString(choice.Delta.ReasoningContent)
		for _, tc := range choice.Delta.ToolCalls {
			a.foldToolCall(tc)
		}
	}
	return text, reasoning
}

// foldToolCall merges a tool-call fragment into the call at its index.
func (a *accumulator) foldToolCall(tc streamToolCall) {
	pos, ok := a.idx[tc.Index]
	if !ok {
		pos = len(a.calls)
		a.idx[tc.Index] = pos
		a.calls = append(a.calls, &accCall{})
	}
	call := a.calls[pos]
	if tc.ID != "" {
		call.id = tc.ID
	}
	if tc.Function.Name != "" {
		call.name = tc.Function.Name
	}
	call.args.WriteString(tc.Function.Arguments)
}

// finalMessage builds the assistant message from accumulated state.
func (a *accumulator) finalMessage() message.Message {
	blocks := make([]message.Block, 0, 2+len(a.calls))
	if reasoning := a.reasoning.String(); reasoning != "" {
		blocks = append(blocks, message.NewThinking(reasoning))
	}
	if text := a.text.String(); text != "" {
		blocks = append(blocks, message.NewText(text))
	}
	for _, call := range a.calls {
		blocks = append(blocks, message.NewToolUse(call.id, call.name, json.RawMessage(call.args.String())))
	}
	return message.Assistant(blocks...)
}
