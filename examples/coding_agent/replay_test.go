package main_test

import (
	"context"
	"os"
	"testing"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/runner"
	"github.com/nethinwei/fino/x/replay"
)

func buildReplayAgent(t *testing.T, log *replay.Log) *agent.Agent {
	t.Helper()
	readFile := replay.ReplayTool("read_file", log)
	listFiles := replay.ReplayTool("list_files", log)
	writeFile := replay.ReplayTool("write_file", log)
	runTests := replay.ReplayTool("run_tests", log)

	planMode, err := agent.NewMode("plan",
		"You are a planning agent.",
		agent.WithTools(readFile, listFiles),
	)
	if err != nil {
		t.Fatalf("NewMode plan: %v", err)
	}
	codeMode, err := agent.NewMode("code",
		"You are an implementation agent.",
		agent.WithTools(readFile, listFiles, writeFile, runTests),
	)
	if err != nil {
		t.Fatalf("NewMode code: %v", err)
	}
	a, err := agent.New("coding",
		agent.WithMode(planMode),
		agent.WithMode(codeMode),
		agent.WithDefaultMode("plan"),
	)
	if err != nil {
		t.Fatalf("New agent: %v", err)
	}
	return a
}

func countSuspendEvents(events []replay.Event) int {
	n := 0
	for _, ev := range events {
		if ev.Kind == replay.EventSuspend {
			n++
		}
	}
	return n
}

func TestReplayPlanCodeSuspendResume(t *testing.T) {
	data, err := os.ReadFile("testdata/plan_code_suspend_resume.json")
	if err != nil {
		t.Skipf("fixture not found (run with DEEPSEEK_API_KEY first): %v", err)
	}
	log, err := replay.Unmarshal(data)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	recAgent := buildReplayAgent(t, log)
	r, err := runner.New(
		&replay.ReplayModel{Log: log},
		runner.WithPolicy(&replay.ReplayPolicy{Log: log}),
		runner.WithMaxConcurrency(2),
	)
	if err != nil {
		t.Fatalf("runner.New: %v", err)
	}

	ctx := context.Background()

	planResult, err := r.Run(ctx, recAgent, runner.Text("plan prompt"), runner.WithMode("plan"))
	if err != nil {
		t.Fatalf("plan Run: %v", err)
	}
	if planResult.Text() == "" {
		t.Fatal("plan result has no text")
	}

	runID := "coding-run-1"
	codeResult, err := r.Run(ctx, recAgent,
		runner.Messages(planResult.Messages),
		runner.WithMode("code"),
		runner.WithRunID(runID),
	)
	if err != nil {
		t.Fatalf("code Run: %v", err)
	}

	suspendCount := 0
	for codeResult.Suspended {
		suspended, serr := codeResult.SuspendedRun()
		if serr != nil {
			t.Fatalf("SuspendedRun: %v", serr)
		}

		approvals := make([]runner.Approval, len(suspended.PendingCalls))
		for i, pc := range suspended.PendingCalls {
			approvals[i] = runner.Approval{
				CallID:   pc.Call.ID,
				Approved: true,
				Reason:   "auto-approved in replay",
			}
		}

		codeResult, err = r.ResumeApproved(ctx, recAgent, suspended, approvals)
		if err != nil {
			t.Fatalf("ResumeApproved: %v", err)
		}
		suspendCount++
	}

	if codeResult.Text() == "" {
		t.Fatal("final result has no text")
	}

	recordedSuspendCount := countSuspendEvents(log.Events)
	if suspendCount != recordedSuspendCount {
		t.Fatalf("replay suspend count = %d, recorded = %d", suspendCount, recordedSuspendCount)
	}
	t.Logf("replay OK: %d suspend(s), final text = %q", suspendCount, codeResult.Text())
}
