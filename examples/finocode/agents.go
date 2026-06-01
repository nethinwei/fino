package main

import (
	"sort"
	"strings"

	"github.com/nethinwei/fino/agent"
)

// agentRegistry tracks the agents and their mode names so the REPL can offer
// /mode and /agents. The core agent.Agent does not expose its mode list, so we
// record it here at build time.
type agentRegistry struct {
	main   *agent.Agent
	agents map[string]*agent.Agent
	modes  map[string][]string
}

func (r *agentRegistry) hasMode(agentName, mode string) bool {
	for _, m := range r.modes[agentName] {
		if m == mode {
			return true
		}
	}
	return false
}

// describe renders the agent/mode catalog for the /agents command.
func (r *agentRegistry) describe() string {
	names := make([]string, 0, len(r.agents))
	for name := range r.agents {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	for _, name := range names {
		role := "subagent"
		if name == r.main.Name() {
			role = "main"
		}
		b.WriteString(name + " (" + role + "): modes [" + strings.Join(r.modes[name], ", ") + "]\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// buildRegistry wires the main "finocode" agent (plan + code modes) and a
// "reviewer" subagent. The code mode can hand off to the reviewer, which reads
// and tests the project then reports back. Control returns to the main agent
// at the start of the next user turn (see session.runTurn), so no reverse
// handoff tool is needed.
func buildRegistry(ws *workspace) (*agentRegistry, error) {
	list, read := ws.listTool(), ws.readTool()
	write, tests, run := ws.writeTool(), ws.runTestsTool(), ws.runProgramTool()

	reviewMode, err := agent.NewMode("review",
		"You are the reviewer subagent. Read the relevant files and run the "+
			"tests, then report a concise verdict: PASS or FAIL with a one-line "+
			"reason. Do not edit files.",
		agent.WithTools(list, read, tests))
	if err != nil {
		return nil, err
	}
	reviewer, err := agent.New("reviewer", agent.WithMode(reviewMode), agent.WithDefaultMode("review"))
	if err != nil {
		return nil, err
	}

	toReviewer, err := agent.NewHandoffTool(reviewer)
	if err != nil {
		return nil, err
	}
	codeMode, err := agent.NewMode("code",
		"You are FinoCode, an interactive Go coding assistant working on a small "+
			"Go project in the current workspace.\n\n"+
			"Tools are OPTIONAL. Only call a tool when the user's request actually "+
			"requires it. For greetings, questions, or general discussion, just "+
			"reply in text and call NO tools. Do not inspect or run the project "+
			"unless asked or clearly necessary for the task.\n\n"+
			"When you do need to act: read_file/list_files to inspect, write_file "+
			"to change code, run_tests/run_program to verify, and hand off to the "+
			"reviewer for an independent check. Keep code valid Go (package main).",
		agent.WithTools(list, read, write, tests, run, toReviewer))
	if err != nil {
		return nil, err
	}
	planMode, err := agent.NewMode("plan",
		"You are FinoCode in planning mode: read-only. Tools are optional; only "+
			"read files when it helps answer. Propose concise plans in text and "+
			"never edit or run anything—ask the user to switch to code mode to "+
			"execute. For casual chat, just reply without calling tools.",
		agent.WithTools(list, read))
	if err != nil {
		return nil, err
	}
	finocode, err := agent.New("finocode",
		agent.WithMode(codeMode), agent.WithMode(planMode), agent.WithDefaultMode("code"))
	if err != nil {
		return nil, err
	}

	return &agentRegistry{
		main:   finocode,
		agents: map[string]*agent.Agent{"finocode": finocode, "reviewer": reviewer},
		modes: map[string][]string{
			"finocode": {"code", "plan"},
			"reviewer": {"review"},
		},
	}, nil
}
