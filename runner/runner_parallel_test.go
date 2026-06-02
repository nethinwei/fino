package runner

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/tool"
)

// batchThenFinal scripts a model that returns one assistant message with the
// given tool-use blocks, then a final text message.
func batchThenFinal(calls []message.Block, finalText string) *scriptedModel {
	return &scriptedModel{responses: []message.Message{
		message.Assistant(calls...),
		message.Assistant(message.NewText(finalText)),
	}}
}

func toolUse(id, name string) message.Block {
	return message.NewToolUse(id, name, json.RawMessage(`{"text":"x"}`))
}

func TestRunParallelExecutesConcurrently(t *testing.T) {
	const n = 3
	var started sync.WaitGroup
	started.Add(n)
	release := make(chan struct{})
	mkTool := func(name string) tool.Tool {
		tl, err := tool.NewFunc(name, "block", func(ctx context.Context, in echoInput) (string, error) {
			started.Done()
			<-release
			return name, nil
		})
		if err != nil {
			t.Fatalf("NewFunc error: %v", err)
		}
		return tl
	}
	tools := []tool.Tool{mkTool("t0"), mkTool("t1"), mkTool("t2")}
	m := batchThenFinal([]message.Block{toolUse("c0", "t0"), toolUse("c1", "t1"), toolUse("c2", "t2")}, "done")
	r, err := New(m, WithMaxConcurrency(n))
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}

	// Release only after all three tools have started: this can only happen if
	// they run concurrently. Serial execution would block on the first tool.
	go func() {
		started.Wait()
		close(release)
	}()

	done := make(chan error, 1)
	go func() {
		_, runErr := r.Run(context.Background(), testAgent(t, tools...), Text("hi"))
		done <- runErr
	}()
	select {
	case runErr := <-done:
		if runErr != nil {
			t.Fatalf("Run error: %v", runErr)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout: tools did not run concurrently")
	}
}

func TestRunParallelPreservesResultOrder(t *testing.T) {
	// c0 sleeps longest so it finishes last, yet its result must stay first.
	mkTool := func(name string, delay time.Duration) tool.Tool {
		tl, err := tool.NewFunc(name, "delay", func(ctx context.Context, in echoInput) (string, error) {
			time.Sleep(delay)
			return name, nil
		})
		if err != nil {
			t.Fatalf("NewFunc error: %v", err)
		}
		return tl
	}
	tools := []tool.Tool{
		mkTool("t0", 30*time.Millisecond),
		mkTool("t1", 10*time.Millisecond),
		mkTool("t2", 0),
	}
	m := batchThenFinal([]message.Block{toolUse("c0", "t0"), toolUse("c1", "t1"), toolUse("c2", "t2")}, "done")
	r, err := New(m, WithMaxConcurrency(3))
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	result, err := r.Run(context.Background(), testAgent(t, tools...), Text("hi"))
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	want := []string{"c0", "c1", "c2"}
	for _, msg := range result.Messages {
		if msg.Role != message.RoleTool {
			continue
		}
		if len(msg.Content) != 3 {
			t.Fatalf("tool message has %d blocks, want 3", len(msg.Content))
		}
		for i, block := range msg.Content {
			if block.ToolUseID != want[i] {
				t.Fatalf("block %d ToolUseID = %q, want %q", i, block.ToolUseID, want[i])
			}
		}
	}
}

func TestRunParallelBoundedConcurrency(t *testing.T) {
	const limit = 2
	var inflight, maxSeen int64
	mkTool := func(name string) tool.Tool {
		tl, err := tool.NewFunc(name, "track", func(ctx context.Context, in echoInput) (string, error) {
			cur := atomic.AddInt64(&inflight, 1)
			for {
				prev := atomic.LoadInt64(&maxSeen)
				if cur <= prev || atomic.CompareAndSwapInt64(&maxSeen, prev, cur) {
					break
				}
			}
			time.Sleep(15 * time.Millisecond)
			atomic.AddInt64(&inflight, -1)
			return name, nil
		})
		if err != nil {
			t.Fatalf("NewFunc error: %v", err)
		}
		return tl
	}
	tools := []tool.Tool{mkTool("t0"), mkTool("t1"), mkTool("t2"), mkTool("t3")}
	m := batchThenFinal([]message.Block{
		toolUse("c0", "t0"), toolUse("c1", "t1"), toolUse("c2", "t2"), toolUse("c3", "t3"),
	}, "done")
	r, err := New(m, WithMaxConcurrency(limit))
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	if _, err := r.Run(context.Background(), testAgent(t, tools...), Text("hi")); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if maxSeen > limit {
		t.Fatalf("max concurrent = %d, want <= %d", maxSeen, limit)
	}
	if maxSeen < limit {
		t.Fatalf("max concurrent = %d, expected to reach %d", maxSeen, limit)
	}
}

func TestRunParallelPolicyDenial(t *testing.T) {
	mkTool := func(name string) tool.Tool {
		tl, _ := tool.NewFunc(name, "echo", func(ctx context.Context, in echoInput) (string, error) {
			return name, nil
		})
		return tl
	}
	tools := []tool.Tool{mkTool("t0"), mkTool("t1")}
	m := batchThenFinal([]message.Block{toolUse("c0", "t0"), toolUse("c1", "t1")}, "done")
	r, err := New(m, WithMaxConcurrency(2), WithPolicy(denyPolicy{}))
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	_, err = r.Run(context.Background(), testAgent(t, tools...), Text("hi"))
	var tde *ToolDeniedError
	if !errors.As(err, &tde) {
		t.Fatalf("error = %v, want ToolDeniedError", err)
	}
}

func TestRunParallelToolErrorFailFast(t *testing.T) {
	boom := errors.New("boom")
	var siblingRan atomic.Bool
	failing, _ := tool.NewFunc("t0", "fail", func(ctx context.Context, in echoInput) (string, error) {
		return "", boom
	})
	sibling, _ := tool.NewFunc("t1", "wait", func(ctx context.Context, in echoInput) (string, error) {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(time.Second):
			siblingRan.Store(true)
			return "t1", nil
		}
	})
	m := batchThenFinal([]message.Block{toolUse("c0", "t0"), toolUse("c1", "t1")}, "done")
	r, err := New(m, WithMaxConcurrency(2))
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	_, err = r.Run(context.Background(), testAgent(t, failing, sibling), Text("hi"))
	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want boom", err)
	}
	if siblingRan.Load() {
		t.Fatal("sibling tool was not cancelled on first error")
	}
}

func TestRunParallelHandoffLastWins(t *testing.T) {
	modeA, _ := agent.NewMode("default", "A")
	targetA, _ := agent.New("targetA", agent.WithMode(modeA), agent.WithDefaultMode("default"))
	modeB, _ := agent.NewMode("default", "B")
	targetB, _ := agent.New("targetB", agent.WithMode(modeB), agent.WithDefaultMode("default"))
	handoffA, _ := agent.NewHandoffTool(targetA)
	handoffB, _ := agent.NewHandoffTool(targetB)
	sourceMode, _ := agent.NewMode("default", "source", agent.WithTools(handoffA, handoffB))
	source, _ := agent.New("source", agent.WithMode(sourceMode), agent.WithDefaultMode("default"))

	m := batchThenFinal([]message.Block{
		message.NewToolUse("c0", "handoff_to_targetA", json.RawMessage(`{}`)),
		message.NewToolUse("c1", "handoff_to_targetB", json.RawMessage(`{}`)),
	}, "final")
	r, err := New(m, WithMaxConcurrency(2))
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	result, err := r.Run(context.Background(), source, Text("hi"))
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.LastAgent != targetB {
		t.Fatalf("LastAgent = %v, want targetB (last handoff wins)", result.LastAgent)
	}
}

func TestStreamParallelEventsAndBatch(t *testing.T) {
	mkTool := func(name string) tool.Tool {
		tl, _ := tool.NewFunc(name, "echo", func(ctx context.Context, in echoInput) (string, error) {
			return name, nil
		})
		return tl
	}
	tools := []tool.Tool{mkTool("t0"), mkTool("t1")}
	m := &streamOnlyModel{
		turns: [][]model.Event{
			{model.TurnMessage{Message: message.Assistant(toolUse("c0", "t0"), toolUse("c1", "t1"))}},
			{model.TurnMessage{Message: message.Assistant(message.NewText("done"))}},
		},
	}
	r, err := New(m, WithMaxConcurrency(2))
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	var toolCalls, toolResults, finals int
	var callOrder []string
	for event, err := range r.Stream(context.Background(), testAgent(t, tools...), Text("hi")) {
		if err != nil {
			t.Fatalf("Stream error: %v", err)
		}
		switch e := event.(type) {
		case model.ToolCall:
			toolCalls++
			callOrder = append(callOrder, e.Call.ID)
		case model.ToolResult:
			toolResults++
		case model.FinalMessage:
			finals++
		}
	}
	if toolCalls != 2 || toolResults != 2 || finals != 1 {
		t.Fatalf("toolCalls=%d toolResults=%d finals=%d, want 2/2/1", toolCalls, toolResults, finals)
	}
	if len(callOrder) != 2 || callOrder[0] != "c0" || callOrder[1] != "c1" {
		t.Fatalf("ToolCall order = %v, want [c0 c1]", callOrder)
	}
}

func TestRunParallelSingleCallUsesSerialPath(t *testing.T) {
	echo, _ := tool.NewFunc("echo", "Echo", func(ctx context.Context, in echoInput) (string, error) {
		return "echo: " + in.Text, nil
	})
	m := batchThenFinal([]message.Block{toolUse("c0", "echo")}, "done")
	r, err := New(m, WithMaxConcurrency(4))
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	result, err := r.Run(context.Background(), testAgent(t, echo), Text("hi"))
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.Text() != "done" {
		t.Fatalf("text = %q, want done", result.Text())
	}
}
