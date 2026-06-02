package tool

import (
	"context"
	"testing"
)

func TestExecutionContextFromEmpty(t *testing.T) {
	ec, ok := ExecutionContextFrom(context.Background())
	if ok {
		t.Fatalf("ExecutionContextFrom on bare context: ok = true, want false")
	}
	if ec != (ExecutionContext{}) {
		t.Fatalf("ExecutionContextFrom on bare context: ec = %+v, want zero", ec)
	}
}

func TestContextWithExecutionContextRoundTrip(t *testing.T) {
	want := ExecutionContext{RunID: "run_1", ToolCallID: "call_1", IdempotencyKey: "run_1:call_1"}
	ctx := ContextWithExecutionContext(context.Background(), want)
	got, ok := ExecutionContextFrom(ctx)
	if !ok {
		t.Fatalf("ExecutionContextFrom after set: ok = false, want true")
	}
	if got != want {
		t.Fatalf("ExecutionContextFrom = %+v, want %+v", got, want)
	}
}
