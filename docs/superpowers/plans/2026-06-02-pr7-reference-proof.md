# PR7: Reference Proof Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build `examples/coding_agent/` — a minimal safe coding-agent example proving all core mechanisms (dual-mode, effects, policy suspend/approve/resume, effect-aware concurrency, idempotency boundary, replay equivalence) work end-to-end.

**Architecture:** One `coding` agent with `plan` (read-only) and `code` (write+test) modes. `approvalPolicy` suspends `write_file` for human approval. CLI drives plan → code → approve → resume loop. Replay test drives the same orchestration with `ReplayModel`/`ReplayTool`/`ReplayPolicy` against a pre-recorded fixture.

**Tech Stack:** Go 1.23+, fino core packages, `x/replay`, `providers/deepseek` for CLI, no new core/x packages.

---

## File Structure

```
examples/coding_agent/
├── main.go          # CLI: plan → code → approval loop + recording
├── tools.go         # Tool constructors (read_file, list_files, write_file, run_tests)
├── replay_test.go   # Replay equivalence test (package coding_agent_test)
└── testdata/
    └── plan_code_suspend_resume.json  # Pre-recorded Log fixture
```

---

### Task 1: Tool Constructors

**Files:**
- Create: `examples/coding_agent/tools.go`

- [ ] **Step 1: Create `tools.go` with all four tool constructors**

```go
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/nethinwei/fino/tool"
)

type pathInput struct {
	Path string `json:"path" jsonschema:"description=file or directory path"`
}

type writeFileInput struct {
	Path    string `json:"path" jsonschema:"description=file path to write"`
	Content string `json:"content" jsonschema:"description=full file content to write"`
}

func newReadFile() (tool.Tool, error) {
	return tool.NewFunc("read_file", "Read the contents of a file.",
		func(_ context.Context, in pathInput) (string, error) {
			data, err := os.ReadFile(in.Path)
			if err != nil {
				return "", fmt.Errorf("read %s: %w", in.Path, err)
			}
			return string(data), nil
		},
		tool.WithEffects(tool.Effects{ReadOnly: true, ParallelSafe: true}),
	)
}

func newListFiles() (tool.Tool, error) {
	return tool.NewFunc("list_files", "List files under a directory.",
		func(_ context.Context, in pathInput) (string, error) {
			entries, err := os.ReadDir(in.Path)
			if err != nil {
				return "", fmt.Errorf("list %s: %w", in.Path, err)
			}
			names := make([]string, len(entries))
			for i, e := range entries {
				names[i] = e.Name()
			}
			return strings.Join(names, "\n"), nil
		},
		tool.WithEffects(tool.Effects{ReadOnly: true, ParallelSafe: true}),
	)
}

func newWriteFile() (tool.Tool, error) {
	return tool.NewFunc("write_file", "Write content to a file.",
		func(_ context.Context, in writeFileInput) (string, error) {
			if err := os.WriteFile(in.Path, []byte(in.Content), 0o644); err != nil {
				return "", fmt.Errorf("write %s: %w", in.Path, err)
			}
			return "wrote " + in.Path, nil
		},
		tool.WithEffects(tool.Effects{Destructive: true, ExternalWrite: true, RequiresApproval: true}),
	)
}

func newRunTests() (tool.Tool, error) {
	return tool.NewFunc("run_tests", "Run go test in a directory.",
		func(_ context.Context, in pathInput) (string, error) {
			cmd := exec.Command("go", "test", "./...")
			cmd.Dir = in.Path
			out, err := cmd.CombinedOutput()
			if err != nil {
				return string(out), nil
			}
			return string(out), nil
		},
		tool.WithEffects(tool.Effects{ExternalWrite: true}),
	)
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./examples/coding_agent`
Expected: compiles (will fail because no main.go yet — that's OK; just check `go vet ./examples/coding_agent/tools.go` doesn't error on the package itself)

Actually, `go build` needs main. Let's defer compilation to Task 2 when main.go exists.

---

### Task 2: Approval Policy

**Files:**
- Create: `examples/coding_agent/tools.go` (append)

- [ ] **Step 1: Add `approvalPolicy` to `tools.go`**

Append to `tools.go`:

```go
import "github.com/nethinwei/fino/policy"

type approvalPolicy struct{}

func (approvalPolicy) Authorize(_ context.Context, req policy.Request) (policy.Decision, error) {
	if req.Tool.Effects.RequiresApproval {
		return policy.Decision{Kind: policy.DecisionSuspend, Reason: "requires human approval"}, nil
	}
	return policy.Decision{Kind: policy.DecisionAllow}, nil
}
```

The `policy` import gets added to the existing import block.

---

### Task 3: Agent Builder

**Files:**
- Create: `examples/coding_agent/tools.go` (append)

- [ ] **Step 1: Add `buildAgent` function to `tools.go`**

```go
import "github.com/nethinwei/fino/agent"

func buildAgent(planTools, codeTools []tool.Tool) (*agent.Agent, error) {
	planMode, err := agent.NewMode("plan",
		"You are a planning agent. Inspect the project and produce a step-by-step plan. Do not modify files.",
		agent.WithTools(planTools...),
	)
	if err != nil {
		return nil, err
	}
	codeMode, err := agent.NewMode("code",
		"You are an implementation agent. Execute the plan by writing files and running tests.",
		agent.WithTools(codeTools...),
	)
	if err != nil {
		return nil, err
	}
	return agent.New("coding",
		agent.WithMode(planMode),
		agent.WithMode(codeMode),
		agent.WithDefaultMode("plan"),
	)
}
```

The `agent` import gets added to the existing import block.

---

### Task 4: CLI Main Loop

**Files:**
- Create: `examples/coding_agent/main.go`

- [ ] **Step 1: Write `main.go`**

```go
package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/nethinwei/fino/agent"
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

	planTools := []tool.Tool{readFile, listFiles}
	codeTools := []tool.Tool{readFile, listFiles, writeFile, runTests}
	a, err := buildAgent(planTools, codeTools)
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
	prompt := "Inspect the current project and outline steps to add a greeting function."
	log.Printf("[plan]   starting plan phase")
	planResult, err := r.Run(ctx, recAgent, runner.Text(prompt), runner.WithMode("plan"))
	if err != nil {
		log.Fatal(err)
	}
	replay.RecordTermination(log_, planResult, err)
	log.Printf("[plan]   %s", planResult.Text())

	runID := "coding-run-1"
	log.Printf("[code]   starting code phase (runID=%s)", runID)
	codeResult, err := r.Run(ctx, recAgent,
		runner.Messages(planResult.Messages),
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

		codeResult, err = r.ResumeApproved(ctx, recAgent, suspended, approvals)
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
```

Wait — I need to check the import. `deepseek.New` returns `*openai.Model`, so I need the `openai` import too. Let me fix:

```go
import (
	"github.com/nethinwei/fino/providers/deepseek"
	"github.com/nethinwei/fino/providers/openai"
)
```

And `newModel` returns `(*openai.Model, error)`.

Also need `tool` import for `tool.Tool` in the tool slices, and `replay` import.

- [ ] **Step 2: Verify it compiles**

Run: `go build ./examples/coding_agent`
Expected: compiles successfully (may need `go mod tidy` if new deps)

- [ ] **Step 3: Commit**

```bash
git add examples/coding_agent/tools.go examples/coding_agent/main.go
git commit -m "feat(example): add coding_agent with plan/code modes and approval loop"
```

---

### Task 5: Replay Test — Agent Builder Helper

**Files:**
- Create: `examples/coding_agent/replay_test.go`

- [ ] **Step 1: Write replay test with agent builder and fixture loading**

```go
package coding_agent_test

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

	// Replay plan phase.
	planResult, err := r.Run(ctx, recAgent, runner.Text("plan prompt"), runner.WithMode("plan"))
	if err != nil {
		t.Fatalf("plan Run: %v", err)
	}
	if planResult.Text() == "" {
		t.Fatal("plan result has no text")
	}

	// Replay code phase — should suspend.
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

	// Assert suspend count matches the recorded tape.
	recordedSuspendCount := countSuspendEvents(log.Events)
	if suspendCount != recordedSuspendCount {
		t.Fatalf("replay suspend count = %d, recorded = %d", suspendCount, recordedSuspendCount)
	}
	t.Logf("replay OK: %d suspend(s), final text = %q", suspendCount, codeResult.Text())
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go vet ./examples/coding_agent`
Expected: no errors

- [ ] **Step 3: Run the test without fixture (should skip)**

Run: `go test ./examples/coding_agent -v -run TestReplayPlanCodeSuspendResume`
Expected: SKIP — fixture not found

- [ ] **Step 4: Commit**

```bash
git add examples/coding_agent/replay_test.go
git commit -m "feat(example): add coding_agent replay equivalence test"
```

---

### Task 6: Record Fixture

This task requires a real DeepSeek API key to run the CLI once and produce the fixture JSON.

- [ ] **Step 1: Create testdata directory**

Run: `mkdir -p examples/coding_agent/testdata`

- [ ] **Step 2: Run the CLI to produce the fixture**

Run: `DEEPSEEK_API_KEY=sk-... go run ./examples/coding_agent`

Interact: approve the write_file call when prompted. This produces `testdata/plan_code_suspend_resume.json`.

- [ ] **Step 3: Verify the replay test passes**

Run: `go test ./examples/coding_agent -v -run TestReplayPlanCodeSuspendResume`
Expected: PASS

- [ ] **Step 4: Commit fixture**

```bash
git add examples/coding_agent/testdata/plan_code_suspend_resume.json
git commit -m "feat(example): add coding_agent replay fixture"
```

---

### Task 7: Final Verification

- [ ] **Step 1: Run full test suite**

Run: `go test ./...`
Expected: all pass

- [ ] **Step 2: Run go vet**

Run: `go vet ./...`
Expected: clean

- [ ] **Step 3: Run gofmt**

Run: `gofmt -l .`
Expected: no output

- [ ] **Step 4: Verify spec success criteria**

Check each criterion from the spec:
1. `go run ./examples/coding_agent` completes plan → code → approve → write → test cycle ✓
2. `go test ./examples/coding_agent` passes without API key (replay only) ✓
3. `go vet` and `gofmt` clean ✓
4. Uses every core mechanism: dual-mode, Effects, Policy suspend/approve/resume, WithMaxConcurrency(2) + ParallelSafe batch, ExecutionContext/WithRunID, RecordingModel/Tool/Policy, replay equivalence ✓
5. Replay test drives Runner directly (not eval.RunWithOptions), asserts suspend count ✓
