package hooks

import (
	"context"
	"errors"
	"testing"

	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/tool"
)

type ctxKey struct{}

// TestAllCallbacksFire exercises every hook field and verifies that the
// context returned by BeforeModel/BeforeTool propagates to later callbacks.
func TestAllCallbacksFire(t *testing.T) {
	var got []string
	h := Hooks{
		BeforeModel: func(ctx context.Context, c ModelCall) context.Context {
			got = append(got, "BeforeModel:"+c.AgentName)
			return context.WithValue(ctx, ctxKey{}, "v")
		},
		AfterModel: func(ctx context.Context, r ModelResult) {
			got = append(got, "AfterModel:"+r.Message.Text())
		},
		BeforeTool: func(ctx context.Context, c ToolCall) context.Context {
			got = append(got, "BeforeTool:"+c.Tool.Name)
			return ctx
		},
		AfterTool: func(ctx context.Context, r ToolResult) {
			got = append(got, "AfterTool:"+r.Result.Text())
		},
		OnError: func(ctx context.Context, err error) {
			got = append(got, "OnError:"+err.Error())
		},
	}

	ctx := h.BeforeModel(context.Background(), ModelCall{AgentName: "a"})
	if ctx.Value(ctxKey{}) != "v" {
		t.Fatalf("BeforeModel context not propagated")
	}
	msg := message.Assistant(message.NewText("hi"))
	h.AfterModel(ctx, ModelResult{Message: &msg})
	h.BeforeTool(ctx, ToolCall{Tool: tool.Info{Name: "echo"}})
	h.AfterTool(ctx, ToolResult{Result: tool.Result{Content: []message.Block{message.NewText("done")}}})
	h.OnError(ctx, errors.New("boom"))

	want := []string{"BeforeModel:a", "AfterModel:hi", "BeforeTool:echo", "AfterTool:done", "OnError:boom"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestZeroValueFieldsNil documents the nil-safe contract: a zero-value Hooks
// has no callbacks set, which the Runner relies on to skip them.
func TestZeroValueFieldsNil(t *testing.T) {
	var h Hooks
	if h.BeforeModel != nil || h.AfterModel != nil ||
		h.BeforeTool != nil || h.AfterTool != nil || h.OnError != nil {
		t.Fatal("zero-value Hooks must have nil fields")
	}
}
