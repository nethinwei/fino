// Command finocode is an interactive coding assistant that showcases the whole
// fino kernel in a Claude Code-style terminal REPL, using only the standard
// library plus fino itself.
//
// It drives a small Go project that lives in a temp directory: the agent reads
// and edits files, then compiles and runs them with the real go toolchain
// (go test / go run via os/exec). Capabilities on display:
//
//   - agent + modes: a main "finocode" agent with code/plan modes
//   - handoff: code mode can delegate to a "reviewer" subagent
//   - tool: list_files / read_file / write_file / run_tests / run_program
//   - policy: interactive y/N authorization with a diff preview for writes
//   - hooks: a dim lifecycle status line (toggle with /verbose)
//   - runner: streaming, bounded parallel tools, turn cap, per-run model opts
//   - model: DeepSeek over the OpenAI-compatible adapter
//
// The agent compiles and runs model-written Go inside the temp directory; the
// permission prompts are the safety gate. Stdin is read in the terminal's
// default (cooked) mode, so the usual Linux line-editing shortcuts work.
//
//	DEEPSEEK_API_KEY=sk-... go run ./examples/finocode
package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync"

	"github.com/nethinwei/fino/providers/deepseek"
	"github.com/nethinwei/fino/providers/openai"
	"github.com/nethinwei/fino/runner"
)

var (
	curMu     sync.Mutex
	curCancel context.CancelFunc
)

func main() {
	m, err := newModel()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	ws, err := newWorkspace()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer ws.cleanup()

	reg, err := buildRegistry(ws)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	reader := bufio.NewReader(os.Stdin)
	r, err := runner.New(m,
		runner.WithPolicy(interactivePolicy{ws: ws, in: reader}),
		runner.WithHooks(traceHooks()),
		runner.WithMaxConcurrency(4),
		runner.WithMaxTurns(16),
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	sess := newSession(reg, r)
	lr := newLineReader(reader)
	lr.cycle = func() string { sess.cycleMode(); return prompt(sess) }
	defer lr.restore()
	installSignalHandler()
	repl(sess, ws, lr)
}

// repl is the read-eval-print loop: slash commands are handled locally, other
// input is sent to the agent as a streamed turn.
func repl(sess *session, ws *workspace, lr *lineReader) {
	printBanner()
	for {
		line, interrupted, ok := lr.readLine(prompt(sess))
		if !ok { // EOF (Ctrl-D)
			return
		}
		if interrupted { // Ctrl-C: abandon the current line
			continue
		}
		input := strings.TrimSpace(line)
		switch {
		case input == "":
			continue
		case strings.HasPrefix(input, "/"):
			if handleCommand(sess, ws, input) {
				return
			}
		default:
			runOneTurn(sess, input)
		}
	}
}

// runOneTurn runs a single turn under a cancelable context so SIGINT interrupts
// just this turn rather than killing the program.
func runOneTurn(sess *session, input string) {
	ctx, cancel := context.WithCancel(context.Background())
	setCancel(cancel)
	defer func() { setCancel(nil); cancel() }()
	sess.runTurn(ctx, input)
}

func prompt(sess *session) string {
	return "\n" + cyan(bold("finocode ("+sess.mode+") ›")) + " "
}

func setCancel(c context.CancelFunc) {
	curMu.Lock()
	curCancel = c
	curMu.Unlock()
}

// installSignalHandler makes SIGINT cancel the active turn (if any) instead of
// terminating the process.
func installSignalHandler() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt)
	go func() {
		for range sigCh {
			curMu.Lock()
			if curCancel != nil {
				curCancel()
			}
			curMu.Unlock()
		}
	}()
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
