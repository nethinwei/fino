package main

import (
	"context"
	"fmt"
	"os"

	"github.com/nethinwei/fino/hooks"
)

// verbose toggles the dim lifecycle status line (see /verbose). Errors are
// always shown regardless.
var verbose bool

// traceHooks returns lifecycle hooks that print a dim status line to stderr,
// keeping the assistant's stdout output clean. It exercises all five hook
// fields so the example demonstrates the full observability surface.
func traceHooks() *hooks.Hooks {
	turn := 0
	return &hooks.Hooks{
		BeforeModel: func(ctx context.Context, c hooks.ModelCall) context.Context {
			turn++
			status(fmt.Sprintf("turn %d  agent=%s mode=%s  msgs=%d tools=%d",
				turn, c.AgentName, c.ModeName, len(c.Messages), len(c.Tools)))
			return ctx
		},
		AfterModel: func(ctx context.Context, r hooks.ModelResult) {
			status("model <- " + blockSummary(r))
		},
		BeforeTool: func(ctx context.Context, c hooks.ToolCall) context.Context {
			status("tool  → " + c.Tool.Name)
			return ctx
		},
		AfterTool: func(ctx context.Context, r hooks.ToolResult) {
			status("tool  ✓ " + r.Tool.Name)
		},
		OnError: func(ctx context.Context, err error) {
			fmt.Fprintln(os.Stderr, red("error: ")+err.Error())
		},
	}
}

// status prints a dim diagnostic line to stderr when verbose mode is on.
func status(s string) {
	if verbose {
		fmt.Fprintln(os.Stderr, dim("  · "+s))
	}
}
