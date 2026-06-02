//go:build record

package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/providers/deepseek"
	"github.com/nethinwei/fino/providers/openai"
	"github.com/nethinwei/fino/runner"
	"github.com/nethinwei/fino/tool"
	"github.com/nethinwei/fino/x/replay"
)

func main() {
	log.SetFlags(log.Ltime | log.Lmicroseconds)

	m, err := newRecordModel()
	if err != nil {
		log.Fatal(err)
	}

	readFile, err := newReadFile()
	if err != nil {
		log.Fatal(err)
	}
	listFiles, err := newListFiles()
	if err != nil {
		log.Fatal(err)
	}
	writeFile, err := newWriteFile()
	if err != nil {
		log.Fatal(err)
	}
	runTests, err := newRunTests()
	if err != nil {
		log.Fatal(err)
	}

	log_ := &replay.Log{}
	recModel := replay.RecordingModel{Next: m, Log: log_}
	recReadFile := replay.RecordingTool(readFile, log_)
	recListFiles := replay.RecordingTool(listFiles, log_)
	recWriteFile := replay.RecordingTool(writeFile, log_)
	recRunTests := replay.RecordingTool(runTests, log_)

	recPlanTools := []tool.Tool{recReadFile, recListFiles}
	recCodeTools := []tool.Tool{recReadFile, recListFiles, recWriteFile, recRunTests}
	recAgent, err := buildAgent(recPlanTools, recCodeTools)
	if err != nil {
		log.Fatal(err)
	}

	recPolicy := replay.RecordingPolicy{Next: approvalPolicy{}, Log: log_}
	r, err := runner.New(recModel, runner.WithPolicy(recPolicy), runner.WithMaxConcurrency(2))
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	prompt := "Read both README.md and CHANGELOG.md at the same time and summarize them briefly."
	log.Printf("[plan]   starting plan phase")
	planResult, err := r.Run(ctx, recAgent, runner.Text(prompt), runner.WithMode("plan"))
	if err != nil {
		log.Fatal(err)
	}
	replay.RecordTermination(log_, planResult, err)
	log.Printf("[plan]   %s", planResult.Text())

	runID := "coding-run-1"
	codePrompt := "Based on the plan, write a file called hello.txt with a greeting. Use the write_file tool."
	codeMessages := append(planResult.Messages, message.UserText(codePrompt))
	log.Printf("[code]   starting code phase (runID=%s)", runID)
	codeResult, err := r.Run(ctx, recAgent,
		runner.Messages(codeMessages),
		runner.WithMode("code"),
		runner.WithRunID(runID),
	)
	if err != nil {
		log.Fatal(err)
	}

	suspendCount := 0
	for codeResult.Suspended {
		suspended, serr := codeResult.SuspendedRun()
		if serr != nil {
			log.Fatal(serr)
		}
		replay.RecordSuspend(log_, suspended)
		suspendCount++

		approvals := make([]runner.Approval, len(suspended.PendingCalls))
		for i, pc := range suspended.PendingCalls {
			log.Printf("[approve] auto-approving %s call %s", pc.Tool.Name, pc.Call.ID)
			approvals[i] = runner.Approval{
				CallID:   pc.Call.ID,
				Approved: true,
				Reason:   "auto-approved for recording",
			}
		}
		replay.RecordApproval(log_, approvals)

		codeResult, err = r.ResumeApproved(ctx, recAgent, suspended, approvals)
		replay.RecordResume(log_, suspended, approvals, codeResult, err)
		if err != nil {
			log.Fatal(err)
		}
		if suspendCount >= 3 {
			log.Printf("[warn] stopping after %d suspends to avoid token overflow", suspendCount)
			break
		}
	}

	replay.RecordTermination(log_, codeResult, err)
	log.Printf("[code]   %s (suspends=%d)", codeResult.Text(), suspendCount)

	data, err := log_.Marshal()
	if err != nil {
		log.Fatal(err)
	}
	fixturePath := "examples/coding_agent/testdata/plan_code_suspend_resume.json"
	if err := os.MkdirAll("examples/coding_agent/testdata", 0o755); err != nil {
		log.Printf("[warn] could not create testdata dir: %v", err)
	}
	if err := os.WriteFile(fixturePath, data, 0o644); err != nil {
		log.Printf("[warn] could not write fixture: %v", err)
	} else {
		log.Printf("[tape]   wrote %s (%d bytes)", fixturePath, len(data))
	}
}

func newRecordModel() (*openai.Model, error) {
	key := os.Getenv("DEEPSEEK_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("set DEEPSEEK_API_KEY")
	}
	name := os.Getenv("DEEPSEEK_MODEL")
	if name == "" {
		name = "deepseek-v4-flash"
	}
	return deepseek.New(name, key)
}
