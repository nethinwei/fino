package main

import (
	"fmt"
	"strings"
)

func printBanner() {
	fmt.Println(bold("finocode") + dim(" — interactive fino coding agent (DeepSeek)"))
	fmt.Println(dim("Describe a change, or use /help for commands. Shift-Tab cycles mode; Ctrl-C interrupts a turn; Ctrl-D quits."))
}

func printHelp() {
	lines := []string{
		"/help            show this help",
		"/files           list project files",
		"/cat <path>      print a file",
		"/mode [name]     show or switch mode (code, plan); Shift-Tab cycles",
		"/agents          list agents and their modes",
		"/verbose         toggle the lifecycle status line",
		"/reset           reset the project and conversation",
		"/exit            quit (or Ctrl-D)",
	}
	for _, l := range lines {
		fmt.Println("  " + l)
	}
}

// handleCommand runs a slash command and reports whether the REPL should quit.
func handleCommand(sess *session, ws *workspace, line string) bool {
	fields := strings.Fields(line)
	cmd := fields[0]
	arg := strings.TrimSpace(strings.TrimPrefix(line, cmd))
	switch cmd {
	case "/exit", "/quit":
		return true
	case "/help":
		printHelp()
	case "/files":
		fmt.Println(strings.Join(ws.list(), "\n"))
	case "/cat":
		cmdCat(ws, arg)
	case "/mode":
		cmdMode(sess, arg)
	case "/agents":
		fmt.Println(sess.reg.describe())
	case "/verbose":
		verbose = !verbose
		printNotice("verbose", boolStr(verbose))
	case "/reset":
		ws.reset()
		sess.history = nil
		printNotice("reset", "project and conversation cleared")
	default:
		fmt.Println(red("unknown command: ") + cmd + dim("  (try /help)"))
	}
	return false
}

func cmdCat(ws *workspace, path string) {
	if path == "" {
		fmt.Println(red("usage: /cat <path>"))
		return
	}
	content, err := ws.read(path)
	if err != nil {
		fmt.Println(red(err.Error()))
		return
	}
	fmt.Print(content)
	if !strings.HasSuffix(content, "\n") {
		fmt.Println()
	}
}

func cmdMode(sess *session, name string) {
	avail := strings.Join(sess.reg.modes[sess.reg.main.Name()], ", ")
	if name == "" {
		printNotice("mode", "current "+sess.mode+", available: "+avail)
		return
	}
	if !sess.reg.hasMode(sess.reg.main.Name(), name) {
		fmt.Println(red("no such mode: ") + name + dim("  ("+avail+")"))
		return
	}
	sess.mode = name
	printNotice("mode", "switched to "+name)
}

func boolStr(b bool) string {
	if b {
		return "on"
	}
	return "off"
}
