package main

import (
	"bufio"
	"context"
	"fmt"
	"strings"

	"github.com/nethinwei/fino/policy"
)

// interactivePolicy gates tool execution like Claude Code: reads and handoffs
// are auto-approved, while write_file (with a diff preview) and the Go exec
// tools require an explicit y/N confirmation on the terminal.
//
// It reads from the same stdin reader the REPL uses. Authorize runs serially
// on the stream's goroutine while the REPL loop is blocked consuming events,
// so sharing the reader is safe.
type interactivePolicy struct {
	ws *workspace
	in *bufio.Reader
}

func (p interactivePolicy) Authorize(ctx context.Context, req policy.Request) (policy.Decision, error) {
	switch {
	case req.Tool.Name == "write_file":
		path := gjsonString(req.Input, "path")
		printDiff(path, p.ws.current(path), gjsonString(req.Input, "content"))
		return p.confirm("apply write to " + path)
	case req.Tool.Name == "run_tests" || req.Tool.Name == "run_program":
		return p.confirm("run " + req.Tool.Name)
	default:
		// read_file, list_files, and handoff tools are auto-approved.
		return policy.Decision{Allow: true}, nil
	}
}

// confirm prompts on the terminal and returns an allow/deny decision. EOF or a
// blank/negative answer denies.
func (p interactivePolicy) confirm(action string) (policy.Decision, error) {
	fmt.Printf("%s %s ", yellow(bold("?")), "allow "+action+" "+dim("[y/N]")+":")
	line, _ := p.in.ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		fmt.Println(green("  approved"))
		return policy.Decision{Allow: true}, nil
	default:
		fmt.Println(red("  denied"))
		return policy.Decision{Allow: false, Reason: "user declined " + action}, nil
	}
}
