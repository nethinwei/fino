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

var parallelSafeEffects = tool.Effects{ParallelSafe: true}

func parallelSafeTool(t *testing.T, name, description string, fn func(context.Context, echoInput) (string, error)) tool.Tool {
	t.Helper()
	tl, err := tool.NewFunc(name, description, fn, tool.WithEffects(parallelSafeEffects))
	if err != nil {
		t.Fatalf("NewFunc %q error: %v", name, err)
	}
	return tl
}

type effectHandoffTool struct {
	agent.HandoffTool
	effects tool.Effects
}

func (h effectHandoffTool) Info() tool.Info {
	info := h.HandoffTool.Info()
	info.Effects = h.effects
	return info
}

func parallelSafeHandoff(t *testing.T, handoff tool.Tool) tool.Tool {
	t.Helper()
	h, ok := handoff.(agent.HandoffTool)
	if !ok {
		t.Fatalf("%T is not a handoff tool", handoff)
	}
	return effectHandoffTool{HandoffTool: h, effects: parallelSafeEffects}
}

// runSteps drives rn through Step calls until done or error.
func runSteps(rn *Run) (*Result, error) {
	for {
		out, err := rn.Step()
		if err != nil {
			return nil, err
		}
		if out.Status == StepCompleted {
			return rn.Result(out.FinalMessage), nil
		}
		if out.Status == StepSuspended {
			return rn.SuspendedResult(out.PendingCalls), nil
		}
	}
}

func TestRunMaxConcurrencyFallsBackToSerialForUnspecifiedEffects(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondStarted := make(chan struct{}, 1)
	t.Cleanup(func() {
		select {
		case <-releaseFirst:
		default:
			close(releaseFirst)
		}
	})

	first, err := tool.NewFunc("first", "first", func(ctx context.Context, in echoInput) (string, error) {
		close(firstStarted)
		<-releaseFirst
		return "first", nil
	})
	if err != nil {
		t.Fatalf("NewFunc first error: %v", err)
	}
	second, err := tool.NewFunc("second", "second", func(ctx context.Context, in echoInput) (string, error) {
		secondStarted <- struct{}{}
		return "second", nil
	})
	if err != nil {
		t.Fatalf("NewFunc second error: %v", err)
	}

	m := batchThenFinal([]message.Block{toolUse("c0", "first"), toolUse("c1", "second")}, "done")
	r, err := New(m, WithMaxConcurrency(2))
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		rn, err := r.NewRun(context.Background(), testAgent(t, first, second), Text("hi"))
		if err != nil {
			done <- err
			return
		}
		_, err = runSteps(rn)
		done <- err
	}()

	<-firstStarted
	select {
	case <-secondStarted:
		t.Fatal("second tool started before first completed; zero-value effects must force serial execution")
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseFirst)
	if err := <-done; err != nil {
		t.Fatalf("Run error: %v", err)
	}
}

func TestRunMaxConcurrencyFallsBackToSerialForMixedParallelSafety(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondStarted := make(chan struct{}, 1)
	t.Cleanup(func() {
		select {
		case <-releaseFirst:
		default:
			close(releaseFirst)
		}
	})

	first := parallelSafeTool(t, "first", "first", func(ctx context.Context, in echoInput) (string, error) {
		close(firstStarted)
		<-releaseFirst
		return "first", nil
	})
	second, err := tool.NewFunc("second", "second", func(ctx context.Context, in echoInput) (string, error) {
		secondStarted <- struct{}{}
		return "second", nil
	})
	if err != nil {
		t.Fatalf("NewFunc second error: %v", err)
	}

	m := batchThenFinal([]message.Block{toolUse("c0", "first"), toolUse("c1", "second")}, "done")
	r, err := New(m, WithMaxConcurrency(2))
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		rn, err := r.NewRun(context.Background(), testAgent(t, first, second), Text("hi"))
		if err != nil {
			done <- err
			return
		}
		_, err = runSteps(rn)
		done <- err
	}()

	<-firstStarted
	select {
	case <-secondStarted:
		t.Fatal("mixed safe/unsafe batch ran concurrently; whole batch must be serial")
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseFirst)
	if err := <-done; err != nil {
		t.Fatalf("Run error: %v", err)
	}
}

func TestRunParallelExecutesConcurrently(t *testing.T) {
	const n = 3
	var started sync.WaitGroup
	started.Add(n)
	release := make(chan struct{})
	mkTool := func(name string) tool.Tool {
		return parallelSafeTool(t, name, "block", func(ctx context.Context, in echoInput) (string, error) {
			started.Done()
			<-release
			return name, nil
		})
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
		rn, err := r.NewRun(context.Background(), testAgent(t, tools...), Text("hi"))
		if err != nil {
			done <- err
			return
		}
		_, err = runSteps(rn)
		done <- err
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
		return parallelSafeTool(t, name, "delay", func(ctx context.Context, in echoInput) (string, error) {
			time.Sleep(delay)
			return name, nil
		})
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
	rn, err := r.NewRun(context.Background(), testAgent(t, tools...), Text("hi"))
	if err != nil {
		t.Fatalf("NewRun error: %v", err)
	}
	result, err := runSteps(rn)
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
		return parallelSafeTool(t, name, "track", func(ctx context.Context, in echoInput) (string, error) {
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
	}
	tools := []tool.Tool{mkTool("t0"), mkTool("t1"), mkTool("t2"), mkTool("t3")}
	m := batchThenFinal([]message.Block{
		toolUse("c0", "t0"), toolUse("c1", "t1"), toolUse("c2", "t2"), toolUse("c3", "t3"),
	}, "done")
	r, err := New(m, WithMaxConcurrency(limit))
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	rn, err := r.NewRun(context.Background(), testAgent(t, tools...), Text("hi"))
	if err != nil {
		t.Fatalf("NewRun error: %v", err)
	}
	if _, err := runSteps(rn); err != nil {
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
		return parallelSafeTool(t, name, "echo", func(ctx context.Context, in echoInput) (string, error) {
			return name, nil
		})
	}
	tools := []tool.Tool{mkTool("t0"), mkTool("t1")}
	m := batchThenFinal([]message.Block{toolUse("c0", "t0"), toolUse("c1", "t1")}, "done")
	r, err := New(m, WithMaxConcurrency(2), WithPolicy(denyPolicy{}))
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}
	rn, err := r.NewRun(context.Background(), testAgent(t, tools...), Text("hi"))
	if err != nil {
		t.Fatalf("NewRun error: %v", err)
	}
	_, err = runSteps(rn)
	var tde *ToolDeniedError
	if !errors.As(err, &tde) {
		t.Fatalf("error = %v, want ToolDeniedError", err)
	}
}

func TestRunParallelToolErrorFailFast(t *testing.T) {
	boom := errors.New("boom")
	var siblingRan atomic.Bool
	failing := parallelSafeTool(t, "t0", "fail", func(ctx context.Context, in echoInput) (string, error) {
		return "", boom
	})
	sibling := parallelSafeTool(t, "t1", "wait", func(ctx context.Context, in echoInput) (string, error) {
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
	rn, err := r.NewRun(context.Background(), testAgent(t, failing, sibling), Text("hi"))
	if err != nil {
		t.Fatalf("NewRun error: %v", err)
	}
	_, err = runSteps(rn)
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
	handoffA = parallelSafeHandoff(t, handoffA)
	handoffB = parallelSafeHandoff(t, handoffB)
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
	rn, err := r.NewRun(context.Background(), source, Text("hi"))
	if err != nil {
		t.Fatalf("NewRun error: %v", err)
	}
	result, err := runSteps(rn)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.LastAgent != targetB {
		t.Fatalf("LastAgent = %v, want targetB (last handoff wins)", result.LastAgent)
	}
}

func TestStreamParallelEventsAndBatch(t *testing.T) {
	mkTool := func(name string) tool.Tool {
		return parallelSafeTool(t, name, "echo", func(ctx context.Context, in echoInput) (string, error) {
			return name, nil
		})
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
	rn, err := r.NewRun(context.Background(), testAgent(t, tools...), Text("hi"))
	if err != nil {
		t.Fatalf("NewRun error: %v", err)
	}
	var toolCalls, toolResults, finals int
	var callOrder []string
	collectEvents := func(ev model.Event, stepErr error) bool {
		if stepErr != nil {
			t.Errorf("Stream error: %v", stepErr)
			return false
		}
		switch e := ev.(type) {
		case model.ToolCall:
			toolCalls++
			callOrder = append(callOrder, e.Call.ID)
		case model.ToolResult:
			toolResults++
		case model.FinalMessage:
			finals++
		}
		return true
	}
	out, err := rn.StreamStep(collectEvents)
	if err != nil || out.Status != StepContinue {
		t.Fatalf("StreamStep 1: err=%v status=%v", err, out.Status)
	}
	if _, err := rn.StreamStep(collectEvents); err != nil {
		t.Fatalf("StreamStep 2 error: %v", err)
	}
	if toolCalls != 2 || toolResults != 2 || finals != 1 {
		t.Fatalf("toolCalls=%d toolResults=%d finals=%d, want 2/2/1", toolCalls, toolResults, finals)
	}
	if len(callOrder) != 2 || callOrder[0] != "c0" || callOrder[1] != "c1" {
		t.Fatalf("ToolCall order = %v, want [c0 c1]", callOrder)
	}
}

func TestStreamMaxConcurrencyFallsBackToSerialForUnspecifiedEffects(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondStarted := make(chan struct{}, 1)
	t.Cleanup(func() {
		select {
		case <-releaseFirst:
		default:
			close(releaseFirst)
		}
	})

	first, err := tool.NewFunc("first", "first", func(ctx context.Context, in echoInput) (string, error) {
		close(firstStarted)
		<-releaseFirst
		return "first", nil
	})
	if err != nil {
		t.Fatalf("NewFunc first error: %v", err)
	}
	second, err := tool.NewFunc("second", "second", func(ctx context.Context, in echoInput) (string, error) {
		secondStarted <- struct{}{}
		return "second", nil
	})
	if err != nil {
		t.Fatalf("NewFunc second error: %v", err)
	}
	m := &streamOnlyModel{turns: [][]model.Event{
		{model.TurnMessage{Message: message.Assistant(toolUse("c0", "first"), toolUse("c1", "second"))}},
		{model.TurnMessage{Message: message.Assistant(message.NewText("done"))}},
	}}
	r, err := New(m, WithMaxConcurrency(2))
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		rn, err := r.NewRun(context.Background(), testAgent(t, first, second), Text("hi"))
		if err != nil {
			done <- err
			return
		}
		var streamErr error
		for {
			out, err := rn.StreamStep(func(ev model.Event, e error) bool {
				if e != nil {
					streamErr = e
					return false
				}
				return true
			})
			if err != nil {
				streamErr = err
				break
			}
			if out.Status != StepContinue {
				break
			}
		}
		done <- streamErr
	}()

	<-firstStarted
	select {
	case <-secondStarted:
		t.Fatal("second stream tool started before first completed; zero-value effects must force serial execution")
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseFirst)
	if err := <-done; err != nil {
		t.Fatalf("Stream error: %v", err)
	}
}

func TestStreamMaxConcurrencyFallsBackToSerialForMixedParallelSafety(t *testing.T) {
	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondStarted := make(chan struct{}, 1)
	t.Cleanup(func() {
		select {
		case <-releaseFirst:
		default:
			close(releaseFirst)
		}
	})

	first := parallelSafeTool(t, "first", "first", func(ctx context.Context, in echoInput) (string, error) {
		close(firstStarted)
		<-releaseFirst
		return "first", nil
	})
	second, err := tool.NewFunc("second", "second", func(ctx context.Context, in echoInput) (string, error) {
		secondStarted <- struct{}{}
		return "second", nil
	})
	if err != nil {
		t.Fatalf("NewFunc second error: %v", err)
	}
	m := &streamOnlyModel{turns: [][]model.Event{
		{model.TurnMessage{Message: message.Assistant(toolUse("c0", "first"), toolUse("c1", "second"))}},
		{model.TurnMessage{Message: message.Assistant(message.NewText("done"))}},
	}}
	r, err := New(m, WithMaxConcurrency(2))
	if err != nil {
		t.Fatalf("New runner error: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		rn, err := r.NewRun(context.Background(), testAgent(t, first, second), Text("hi"))
		if err != nil {
			done <- err
			return
		}
		var streamErr error
		for {
			out, err := rn.StreamStep(func(ev model.Event, e error) bool {
				if e != nil {
					streamErr = e
					return false
				}
				return true
			})
			if err != nil {
				streamErr = err
				break
			}
			if out.Status != StepContinue {
				break
			}
		}
		done <- streamErr
	}()

	<-firstStarted
	select {
	case <-secondStarted:
		t.Fatal("mixed safe/unsafe stream batch ran concurrently; whole batch must be serial")
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseFirst)
	if err := <-done; err != nil {
		t.Fatalf("Stream error: %v", err)
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
	rn, err := r.NewRun(context.Background(), testAgent(t, echo), Text("hi"))
	if err != nil {
		t.Fatalf("NewRun error: %v", err)
	}
	result, err := runSteps(rn)
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if result.Text() != "done" {
		t.Fatalf("text = %q, want done", result.Text())
	}
}
