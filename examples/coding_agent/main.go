//go:build !record

package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strings"
	"time"

	"github.com/nethinwei/fino/hooks"
	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
	"github.com/nethinwei/fino/providers/deepseek"
	"github.com/nethinwei/fino/providers/openai"
	"github.com/nethinwei/fino/runner"
	"github.com/nethinwei/fino/tool"
	"github.com/nethinwei/fino/x/replay"
)

func main() {
	log.SetFlags(log.Ltime | log.Lmicroseconds)

	m, err := newModel()
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

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	prompt := "Inspect the current project and outline steps to add a greeting function."
	log.Printf("[plan]   starting plan phase (streaming)")

	planHistory := []message.Message{message.UserText(prompt)}
	turn := 0
	var pendingToolResults []message.Block
	for event, err := range r.Stream(ctx, recAgent, runner.Text(prompt), runner.WithMode("plan")) {
		if err != nil {
			log.Fatalf("[plan]   stream error: %v", err)
		}
		switch ev := event.(type) {
		case model.TextDelta:
			fmt.Print(ev.Text)
		case model.ContentBlockStart:
			if ev.Block.Type == message.TypeToolUse {
				fmt.Println()
				log.Printf("[plan]   tool_use: %s", ev.Block.Name)
			}
		case model.ToolCall:
			input := string(ev.Call.Input)
			if len(input) > 120 {
				input = input[:120] + "..."
			}
			log.Printf("[plan]   calling %s(%s)", ev.Call.Name, input)
		case model.ToolResult:
			out := ev.Result.Text()
			if len(out) > 200 {
				out = out[:200] + "..."
			}
			if ev.Result.IsError {
				log.Printf("[plan]   %s ERROR: %s", ev.Name, out)
			} else {
				log.Printf("[plan]   %s -> %s", ev.Name, out)
			}
			pendingToolResults = append(pendingToolResults,
				message.NewToolResult(ev.CallID, ev.Name, ev.Result.Content, ev.Result.IsError))
		case model.TurnMessage:
			turn++
			if len(pendingToolResults) > 0 {
				planHistory = append(planHistory, message.ToolResults(pendingToolResults...))
				pendingToolResults = nil
			}
			planHistory = append(planHistory, ev.Message)
			calls := ev.Message.ToolUses()
			if len(calls) == 0 {
				fmt.Println()
			}
		case model.FinalMessage:
			if len(pendingToolResults) > 0 {
				planHistory = append(planHistory, message.ToolResults(pendingToolResults...))
				pendingToolResults = nil
			}
			planHistory = append(planHistory, ev.Message)
		}
	}

	replay.RecordTermination(log_, nil, nil)
	log.Printf("[plan]   completed in %d turn(s)", turn)

	runID := "coding-run-1"
	log.Printf("[code]   starting code phase (runID=%s)", runID)

	r2, err := runner.New(recModel, runner.WithPolicy(recPolicy), runner.WithMaxConcurrency(2), runner.WithHooks(codeHooks()))
	if err != nil {
		log.Fatal(err)
	}

	codeResult, err := r2.Run(ctx, recAgent,
		runner.Messages(planHistory),
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

		fmt.Println("\n--- pending write ---")
		for _, pc := range suspended.PendingCalls {
			fmt.Printf("  tool: %s\n", pc.Tool.Name)
			fmt.Printf("  call: %s\n", pc.Call.ID)
			fmt.Printf("  input: %s\n", string(pc.Call.Input))
		}
		fmt.Print("\nApprove? [y/n]: ")

		reader := bufio.NewReader(os.Stdin)
		line, _ := reader.ReadString('\n')
		line = strings.TrimSpace(strings.ToLower(line))
		approved := line == "y" || line == "yes"

		approvals := make([]runner.Approval, len(suspended.PendingCalls))
		for i, pc := range suspended.PendingCalls {
			approvals[i] = runner.Approval{
				CallID:   pc.Call.ID,
				Approved: approved,
				Reason:   "human approval",
			}
		}
		replay.RecordApproval(log_, approvals)

		codeResult, err = r2.ResumeApproved(ctx, recAgent, suspended, approvals)
		replay.RecordResume(log_, suspended, approvals, codeResult, err)
		if err != nil {
			log.Fatal(err)
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

func newModel() (*openai.Model, error) {
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

func codeHooks() *hooks.Hooks {
	turn := 0
	return &hooks.Hooks{
		BeforeModel: func(ctx context.Context, c hooks.ModelCall) context.Context {
			turn++
			toolNames := make([]string, len(c.Tools))
			for i, t := range c.Tools {
				toolNames[i] = t.Name
			}
			log.Printf("[code]   turn %d -> mode=%s tools=%v msg_count=%d",
				turn, c.ModeName, toolNames, len(c.Messages))
			return ctx
		},
		AfterModel: func(ctx context.Context, r hooks.ModelResult) {
			text := r.Message.Text()
			if len(text) > 200 {
				text = text[:200] + "..."
			}
			calls := r.Message.ToolUses()
			if len(calls) > 0 {
				names := make([]string, len(calls))
				for i, c := range calls {
					names[i] = c.Name
				}
				log.Printf("[code]   turn %d <- text=%q tool_calls=%v", turn, text, names)
				for _, c := range calls {
					input := string(c.Input)
					if len(input) > 120 {
						input = input[:120] + "..."
					}
					log.Printf("[code]     tool_use: %s(%s)", c.Name, input)
				}
			} else {
				log.Printf("[code]   turn %d <- text=%q", turn, text)
			}
		},
		BeforeTool: func(ctx context.Context, c hooks.ToolCall) context.Context {
			input := string(c.Input)
			if len(input) > 120 {
				input = input[:120] + "..."
			}
			log.Printf("[code]   calling %s(%s)", c.Tool.Name, input)
			return ctx
		},
		AfterTool: func(ctx context.Context, r hooks.ToolResult) {
			out := r.Result.Text()
			if len(out) > 200 {
				out = out[:200] + "..."
			}
			if r.Result.IsError {
				log.Printf("[code]   %s ERROR: %s", r.Tool.Name, out)
			} else {
				log.Printf("[code]   %s -> %s", r.Tool.Name, out)
			}
		},
		OnError: func(ctx context.Context, err error) {
			log.Printf("[error]  %v", err)
		},
	}
}
