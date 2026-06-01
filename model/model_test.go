package model

import (
	"context"
	"iter"
	"testing"

	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/tool"
)

type fakeModel struct{}

func (fakeModel) Generate(ctx context.Context, messages []message.Message, tools []tool.Info, opts ...Option) (*message.Message, error) {
	msg := message.Assistant(message.NewText("ok"))
	return &msg, nil
}

func (fakeModel) Stream(ctx context.Context, messages []message.Message, tools []tool.Info, opts ...Option) iter.Seq2[Event, error] {
	return func(yield func(Event, error) bool) {
		yield(ContentBlockStart{Index: 0, Block: message.NewText("")}, nil)
		yield(TextDelta{Text: "ok"}, nil)
		yield(ContentBlockStop{Index: 0, Block: message.NewText("ok")}, nil)
		yield(FinalMessage{Message: message.Assistant(message.NewText("ok"))}, nil)
	}
}

func TestModelInterface(t *testing.T) {
	var _ Model = fakeModel{}
}

func TestOptions(t *testing.T) {
	cfg := newConfig([]Option{WithTemperature(0.7), WithMaxTokens(10)})
	if cfg.temperature == nil || *cfg.temperature != 0.7 {
		t.Fatal("temperature not applied")
	}
	if cfg.maxTokens == nil || *cfg.maxTokens != 10 {
		t.Fatal("max tokens not applied")
	}
}
