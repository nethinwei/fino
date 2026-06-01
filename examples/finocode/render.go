package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/nethinwei/fino/hooks"
	"github.com/nethinwei/fino/message"
	"github.com/nethinwei/fino/model"
)

// colorEnabled gates ANSI escapes; set NO_COLOR to disable.
var colorEnabled = os.Getenv("NO_COLOR") == ""

func wrap(code, s string) string {
	if !colorEnabled {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

func bold(s string) string   { return wrap("1", s) }
func dim(s string) string    { return wrap("2", s) }
func red(s string) string    { return wrap("31", s) }
func green(s string) string  { return wrap("32", s) }
func yellow(s string) string { return wrap("33", s) }
func cyan(s string) string   { return wrap("36", s) }

// renderer prints stream events to stdout incrementally so assistant text
// visibly flows. It writes directly to os.Stdout (no buffering) so it stays
// correctly ordered with the interactive policy prompts. Reasoning arrives
// first (as ContentBlockDelta thinking) and is shown dim, then answer text.
type renderer struct {
	textOpen     bool
	thinkingOpen bool
}

func (r *renderer) render(ev model.Event) {
	switch e := ev.(type) {
	case model.ContentBlockDelta:
		if e.Block.Type == message.TypeThinking {
			if !r.thinkingOpen {
				r.endText()
				fmt.Print(dim("\nthinking "))
				r.thinkingOpen = true
			}
			fmt.Print(dim(e.Block.Text))
		}
	case model.TextDelta:
		// The header's leading newline closes any open reasoning line, so we
		// only clear the flag here to avoid emitting a second blank line.
		r.thinkingOpen = false
		if !r.textOpen {
			fmt.Print(cyan(bold("\nfinocode ")))
			r.textOpen = true
		}
		fmt.Print(e.Text)
	case model.ToolCall:
		r.endText()
		fmt.Printf("%s %s%s\n", yellow("·"), bold(e.Call.Name), dim("("+oneLine(string(e.Call.Input))+")"))
	case model.ToolResult:
		fmt.Printf("  %s %s\n", dim("⤷"), dim(oneLine(e.Result.Text())))
	case model.Handoff:
		r.endText()
		printNotice("handoff → subagent", e.Target)
	case model.FinalMessage:
		// Text and reasoning already streamed via TextDelta/ContentBlockDelta.
	}
}

// endThinking closes an open dim reasoning section with a newline.
func (r *renderer) endThinking() {
	if r.thinkingOpen {
		fmt.Println()
		r.thinkingOpen = false
	}
}

func (r *renderer) endText() {
	r.endThinking()
	if r.textOpen {
		fmt.Println()
		r.textOpen = false
	}
}

func (r *renderer) finish() { r.endText() }

// printNotice prints a harness banner, e.g. mode switches and handoffs.
func printNotice(label, detail string) {
	fmt.Printf("\n%s %s\n", cyan(bold("» "+label)), dim(detail))
}

// printDiff shows a colored line diff between old and new file content.
func printDiff(path, oldContent, newContent string) {
	fmt.Printf("%s %s\n", bold("diff"), path)
	if oldContent == "" {
		fmt.Println(dim("  (new file)"))
	}
	for _, dl := range lineDiff(splitLines(oldContent), splitLines(newContent)) {
		switch dl.kind {
		case '+':
			fmt.Println(green("+ " + dl.text))
		case '-':
			fmt.Println(red("- " + dl.text))
		default:
			fmt.Println(dim("  " + dl.text))
		}
	}
}

type dline struct {
	kind byte
	text string
}

// lineDiff computes a minimal line diff via an LCS table.
func lineDiff(a, b []string) []dline {
	n, m := len(a), len(b)
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] >= dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	var out []dline
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case a[i] == b[j]:
			out = append(out, dline{' ', a[i]})
			i, j = i+1, j+1
		case dp[i+1][j] >= dp[i][j+1]:
			out = append(out, dline{'-', a[i]})
			i++
		default:
			out = append(out, dline{'+', b[j]})
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, dline{'-', a[i]})
	}
	for ; j < m; j++ {
		out = append(out, dline{'+', b[j]})
	}
	return out
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(s, "\n"), "\n")
}

// blockSummary describes the blocks a model turn produced, e.g.
// "thinking, tool_use:write_file", for the dim status hooks.
func blockSummary(res hooks.ModelResult) string {
	if res.Message == nil {
		return "<nil>"
	}
	parts := make([]string, 0, len(res.Message.Content))
	for _, b := range res.Message.Content {
		switch b.Type {
		case message.TypeToolUse:
			parts = append(parts, "tool_use:"+b.Name)
		case message.TypeText:
			parts = append(parts, fmt.Sprintf("text(%dB)", len(b.Text)))
		case message.TypeThinking:
			parts = append(parts, fmt.Sprintf("thinking(%dB)", len(b.Text)))
		default:
			parts = append(parts, string(b.Type))
		}
	}
	if len(parts) == 0 {
		return "(empty)"
	}
	return strings.Join(parts, ", ")
}

// oneLine collapses whitespace and truncates on a rune boundary so multi-byte
// characters (e.g. CJK) are never split into invalid UTF-8.
func oneLine(s string) string {
	s = strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")
	if r := []rune(s); len(r) > 100 {
		return string(r[:97]) + "..."
	}
	return s
}
