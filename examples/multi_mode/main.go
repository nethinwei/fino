// Command multi_mode shows one agent with two modes — "plan" and "code" — and
// how to select a mode per run with runner.WithMode. Modes carry different
// instructions and tool sets.
//
//	DEEPSEEK_API_KEY=sk-... go run ./examples/multi_mode
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/hooks"
	"github.com/nethinwei/fino/providers/deepseek"
	"github.com/nethinwei/fino/providers/openai"
	"github.com/nethinwei/fino/runner"
	"github.com/nethinwei/fino/tool"
)

type pathInput struct {
	Path string `json:"path" jsonschema:"description=file path"`
}

func main() {
	log.SetFlags(log.Ltime)

	m, err := newModel()
	if err != nil {
		log.Fatal(err)
	}

	a, err := buildAgent()
	if err != nil {
		log.Fatal(err)
	}

	// Hooks log the active mode and tool availability per turn, so you can see
	// the plan mode (read-only) vs code mode (write-capable) tool sets differ.
	r, err := runner.New(m, runner.WithHooks(&hooks.Hooks{
		BeforeModel: func(ctx context.Context, c hooks.ModelCall) context.Context {
			log.Printf("[model] mode=%s sees %d tool(s)", c.ModeName, len(c.Tools))
			return ctx
		},
		BeforeTool: func(ctx context.Context, c hooks.ToolCall) context.Context {
			log.Printf("[hook]  tool %q invoked with %s", c.Tool.Name, string(c.Input))
			return ctx
		},
	}))
	if err != nil {
		log.Fatal(err)
	}

	ctx := context.Background()
	log.Print("=== run 1: plan mode ===")
	plan, err := r.Run(ctx, a, runner.Text("Outline the steps to add a feature."), runner.WithMode("plan"))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("[plan]", plan.Text())

	log.Print("=== run 2: code mode ===")
	code, err := r.Run(ctx, a, runner.Text("Now implement step one."), runner.WithMode("code"))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("[code]", code.Text())
}

// buildAgent assembles an agent with read-only "plan" and write-capable "code"
// modes sharing the same model.
func buildAgent() (*agent.Agent, error) {
	listFiles, err := tool.NewFunc("list_files", "List files under a path.",
		func(ctx context.Context, in pathInput) (string, error) {
			log.Printf("[tool] list_files(path=%q)", in.Path)
			return "main.go\nREADME.md", nil
		})
	if err != nil {
		return nil, err
	}
	writeFile, err := tool.NewFunc("write_file", "Write content to a file.",
		func(ctx context.Context, in pathInput) (string, error) {
			log.Printf("[tool] write_file(path=%q)", in.Path)
			return "wrote " + in.Path, nil
		})
	if err != nil {
		return nil, err
	}

	plan, err := agent.NewMode("plan",
		"You are a planner. Inspect the project and produce a step-by-step plan. Do not modify files.",
		agent.WithTools(listFiles))
	if err != nil {
		return nil, err
	}
	code, err := agent.NewMode("code",
		"You are an implementer. Execute the plan by editing files.",
		agent.WithTools(listFiles, writeFile))
	if err != nil {
		return nil, err
	}
	return agent.New("dev",
		agent.WithMode(plan), agent.WithMode(code), agent.WithDefaultMode("plan"))
}

// newModel builds a DeepSeek model from environment variables.
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
