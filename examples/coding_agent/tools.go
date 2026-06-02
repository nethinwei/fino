package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/policy"
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
	return tool.NewFunc("read_file", "Read a file and return its content.",
		func(_ context.Context, in pathInput) (string, error) {
			data, err := os.ReadFile(in.Path)
			if err != nil {
				return fmt.Sprintf("error: %v", err), nil
			}
			return string(data), nil
		},
		tool.WithEffects(tool.Effects{ReadOnly: true, ParallelSafe: true}),
	)
}

func newListFiles() (tool.Tool, error) {
	return tool.NewFunc("list_files", "List files and directories under a path.",
		func(_ context.Context, in pathInput) (string, error) {
			entries, err := os.ReadDir(in.Path)
			if err != nil {
				return fmt.Sprintf("error: %v", err), nil
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
				return fmt.Sprintf("error: %v", err), nil
			}
			return "wrote " + in.Path, nil
		},
		tool.WithEffects(tool.Effects{Destructive: true, ExternalWrite: true, RequiresApproval: true}),
	)
}

func newRunTests() (tool.Tool, error) {
	return tool.NewFunc("run_tests", "Run go test in the given directory.",
		func(_ context.Context, in pathInput) (string, error) {
			cmd := exec.Command("go", "test", "./...")
			cmd.Dir = in.Path
			out, _ := cmd.CombinedOutput()
			return string(out), nil
		},
		tool.WithEffects(tool.Effects{ExternalWrite: true}),
	)
}

type approvalPolicy struct{}

func (approvalPolicy) Authorize(_ context.Context, req policy.Request) (policy.Decision, error) {
	if req.Tool.Effects.RequiresApproval {
		return policy.Decision{Kind: policy.DecisionSuspend, Reason: "requires human approval"}, nil
	}
	return policy.Decision{Kind: policy.DecisionAllow}, nil
}

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
