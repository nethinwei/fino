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
	cfg := newConfig([]Option{WithTemperature(0.7), WithMaxTokens(10), WithTopP(0.9)})
	if cfg.temperature == nil || *cfg.temperature != 0.7 {
		t.Fatal("temperature not applied")
	}
	if cfg.maxTokens == nil || *cfg.maxTokens != 10 {
		t.Fatal("max tokens not applied")
	}
	if cfg.topP == nil || *cfg.topP != 0.9 {
		t.Fatal("top_p not applied")
	}
}

type testKey struct{}

func TestExtraOption(t *testing.T) {
	cfg := ApplyOptions(WithExtra(testKey{}, "high"))
	got, ok := ExtraValue[string](cfg, testKey{})
	if !ok || got != "high" {
		t.Fatalf("ExtraValue = %q, %v; want high, true", got, ok)
	}
	// Wrong type yields the zero value and false.
	if n, ok := ExtraValue[int](cfg, testKey{}); ok || n != 0 {
		t.Fatalf("ExtraValue[int] = %d, %v; want 0, false", n, ok)
	}
	// Absent key on an empty config is safe (nil map).
	if _, ok := ExtraValue[string](ApplyOptions(), testKey{}); ok {
		t.Fatal("absent key reported present")
	}
}
