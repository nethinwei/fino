package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// lineReader provides readline-style editing with zero external dependencies.
// On a TTY it switches the terminal to raw mode via the system `stty` command
// and parses key bytes itself: left/right cursor, Ctrl-A/E/B/F, Up/Down and
// Ctrl-P/Ctrl-N history, Ctrl-U/K/W kills, and backspace. Enter (CR or LF)
// submits; Alt+Enter (ESC then Enter) inserts a newline for multi-line input.
// When stdin is not a TTY (e.g. piped input) it falls back to plain buffered
// line reading so scripts keep working.
//
// Multi-line editing limitation: cursor motion is exact on a single line; once
// the buffer contains newlines, redraw is best-effort (no auto-wrap accounting)
// and the cursor stays at the end. This keeps the editor dependency-free and
// small.
type lineReader struct {
	in       *bufio.Reader
	history  []string
	isTTY    bool
	orig     string
	lastRows int
	// cycle, if set, is invoked on Shift+Tab to advance the mode; it returns the
	// new prompt so the editable line repaints with the updated label in place.
	cycle func() string
}

func newLineReader(in *bufio.Reader) *lineReader {
	lr := &lineReader{in: in}
	if orig, err := runStty("-g"); err == nil {
		lr.isTTY = true
		lr.orig = strings.TrimSpace(orig)
	}
	return lr
}

// readLine prints prompt and returns the entered line. interrupted is true on
// Ctrl-C (the caller should reprint the prompt); ok is false on EOF.
func (lr *lineReader) readLine(prompt string) (line string, interrupted bool, ok bool) {
	if !lr.isTTY {
		fmt.Print(prompt)
		s, err := lr.in.ReadString('\n')
		if err != nil && s == "" {
			return "", false, false
		}
		return strings.TrimRight(s, "\r\n"), false, true
	}
	return lr.edit(prompt)
}

// restore puts the terminal back into cooked mode (used on shutdown).
func (lr *lineReader) restore() {
	if lr.isTTY && lr.orig != "" {
		runStty(strings.Fields(lr.orig)...)
	}
}

func runStty(args ...string) (string, error) {
	cmd := exec.Command("stty", args...)
	cmd.Stdin = os.Stdin
	var out strings.Builder
	cmd.Stdout = &out
	cmd.Stderr = nil // discard "not a terminal" noise
	err := cmd.Run()
	return out.String(), err
}

// edit runs the raw-mode editing loop for one line.
func (lr *lineReader) edit(prompt string) (string, bool, bool) {
	// -icrnl keeps Enter as CR (not translated to LF); onKey treats both CR and
	// LF as submit, so Enter works regardless of the terminal.
	runStty("-echo", "-icanon", "-isig", "-ixon", "-icrnl", "min", "1", "time", "0")
	defer lr.restore()
	// Cosmetic leading newlines print once; render repaints only the editable
	// prompt (without them), so a keystroke does not push the line down.
	lead, p := splitPromptLead(prompt)
	fmt.Print(lead)
	e := &editor{hist: lr.history, histIdx: len(lr.history)}
	lr.lastRows = 1
	fmt.Print(p)
	for {
		r, _, err := lr.in.ReadRune()
		if err != nil {
			fmt.Print("\r\n")
			return "", false, false
		}
		switch e.onKey(r, lr.in) {
		case actSubmit:
			fmt.Print("\r\n")
			line := string(e.buf)
			lr.addHistory(line)
			return line, false, true
		case actEOF:
			fmt.Print("\r\n")
			return "", false, false
		case actInterrupt:
			fmt.Print("^C\r\n")
			return "", true, true
		case actCycleMode:
			if lr.cycle != nil {
				_, p = splitPromptLead(lr.cycle())
			}
			fallthrough
		default:
			lr.render(p, e.buf, e.pos)
		}
	}
}

// splitPromptLead separates leading newlines (cosmetic spacing) from the
// editable prompt that render repaints on every keystroke.
func splitPromptLead(prompt string) (lead, rest string) {
	rest = prompt
	for strings.HasPrefix(rest, "\n") {
		lead += "\n"
		rest = rest[1:]
	}
	return lead, rest
}

func (lr *lineReader) addHistory(line string) {
	if strings.TrimSpace(line) == "" {
		return
	}
	if n := len(lr.history); n > 0 && lr.history[n-1] == line {
		return
	}
	lr.history = append(lr.history, line)
}

// render repaints the input region. Single-line edits position the cursor
// exactly; multi-line buffers are repainted with the cursor left at the end.
func (lr *lineReader) render(prompt string, buf []rune, pos int) {
	if lr.lastRows > 1 {
		fmt.Printf("\x1b[%dA", lr.lastRows-1) // move up to the first row
	}
	fmt.Print("\r\x1b[J") // column 0, clear to end of screen
	fmt.Print(prompt + string(buf))
	lr.lastRows = 1 + strings.Count(string(buf), "\n")
	if lr.lastRows == 1 {
		if back := len(buf) - pos; back > 0 {
			fmt.Printf("\x1b[%dD", back)
		}
	}
}

// action is the outcome of handling a single key.
type action int

const (
	actNone action = iota
	actSubmit
	actEOF
	actInterrupt
	actCycleMode
)

// editor holds the mutable state of one line being edited.
type editor struct {
	buf     []rune
	pos     int
	hist    []string
	histIdx int
	draft   string
}

// onKey applies one decoded rune (reading more bytes from in for escape
// sequences) and reports whether the line is complete.
func (e *editor) onKey(r rune, in *bufio.Reader) action {
	switch r {
	case '\r', '\n': // Enter submits, whether the terminal sends CR or LF
		return actSubmit
	case 0x03:
		return actInterrupt
	case 0x04:
		if len(e.buf) == 0 {
			return actEOF
		}
		e.deleteForward()
	case 0x7f, 0x08:
		e.backspace()
	case 0x01:
		e.pos = 0
	case 0x05:
		e.pos = len(e.buf)
	case 0x02:
		if e.pos > 0 {
			e.pos--
		}
	case 0x06:
		if e.pos < len(e.buf) {
			e.pos++
		}
	case 0x10:
		e.histPrev()
	case 0x0e:
		e.histNext()
	case 0x15:
		e.buf, e.pos = e.buf[e.pos:], 0
	case 0x0b:
		e.buf = e.buf[:e.pos]
	case 0x17:
		e.killWord()
	case 0x1b:
		return e.onEscape(in)
	default:
		if r >= 0x20 {
			e.insert(r)
		}
	}
	return actNone
}

// onEscape decodes an escape sequence (arrows, Home/End, Delete, Shift+Tab) or
// Meta+Enter, returning actCycleMode for Shift+Tab and actNone otherwise.
func (e *editor) onEscape(in *bufio.Reader) action {
	b, _, err := in.ReadRune()
	if err != nil {
		return actNone
	}
	if b == '\r' || b == '\n' { // Alt/Meta+Enter -> newline
		e.insertNewline()
		return actNone
	}
	if b != '[' && b != 'O' {
		return actNone
	}
	c, _, err := in.ReadRune()
	if err != nil {
		return actNone
	}
	switch c {
	case 'Z': // Shift+Tab (CSI Z, back-tab)
		return actCycleMode
	case 'A':
		e.histPrev()
	case 'B':
		e.histNext()
	case 'C':
		if e.pos < len(e.buf) {
			e.pos++
		}
	case 'D':
		if e.pos > 0 {
			e.pos--
		}
	case 'H':
		e.pos = 0
	case 'F':
		e.pos = len(e.buf)
	case '1', '3', '4', '7', '8':
		e.extendedSeq(c, in)
	}
	return actNone
}

// extendedSeq handles CSI sequences ending in '~' (Home/End/Delete).
func (e *editor) extendedSeq(first rune, in *bufio.Reader) {
	for {
		c, _, err := in.ReadRune()
		if err != nil || c == '~' {
			break
		}
	}
	switch first {
	case '1', '7':
		e.pos = 0
	case '4', '8':
		e.pos = len(e.buf)
	case '3':
		e.deleteForward()
	}
}

func (e *editor) insert(r rune) {
	e.buf = append(e.buf, 0)
	copy(e.buf[e.pos+1:], e.buf[e.pos:])
	e.buf[e.pos] = r
	e.pos++
}

func (e *editor) insertNewline() { e.insert('\n') }

func (e *editor) backspace() {
	if e.pos > 0 {
		e.buf = append(e.buf[:e.pos-1], e.buf[e.pos:]...)
		e.pos--
	}
}

func (e *editor) deleteForward() {
	if e.pos < len(e.buf) {
		e.buf = append(e.buf[:e.pos], e.buf[e.pos+1:]...)
	}
}

func (e *editor) killWord() {
	i := e.pos
	for i > 0 && e.buf[i-1] == ' ' {
		i--
	}
	for i > 0 && e.buf[i-1] != ' ' {
		i--
	}
	e.buf = append(e.buf[:i], e.buf[e.pos:]...)
	e.pos = i
}

func (e *editor) histPrev() {
	if len(e.hist) == 0 || e.histIdx == 0 {
		return
	}
	if e.histIdx == len(e.hist) {
		e.draft = string(e.buf)
	}
	e.histIdx--
	e.setBuf(e.hist[e.histIdx])
}

func (e *editor) histNext() {
	if e.histIdx >= len(e.hist) {
		return
	}
	e.histIdx++
	if e.histIdx == len(e.hist) {
		e.setBuf(e.draft)
	} else {
		e.setBuf(e.hist[e.histIdx])
	}
}

func (e *editor) setBuf(s string) {
	e.buf = []rune(s)
	e.pos = len(e.buf)
}
