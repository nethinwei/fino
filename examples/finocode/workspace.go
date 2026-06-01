package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nethinwei/fino/tool"
)

const (
	// goCommandTimeout bounds each go test/run so model-written code that hangs
	// (e.g. an infinite loop) cannot wedge a turn indefinitely.
	goCommandTimeout = 30 * time.Second
	// maxGoOutput caps the combined output fed back to the model as a tool result.
	maxGoOutput = 4000
)

// workspace is the project the agent edits. The in-memory map is the source of
// truth; every write is mirrored to a real temp directory so the Go toolchain
// can compile and run the code. The mutex keeps parallel tool calls race-free.
type workspace struct {
	mu    sync.Mutex
	dir   string
	files map[string]string
}

// seedFiles is a minimal Go module whose tests fail until Add is implemented.
func seedFiles() map[string]string {
	return map[string]string{
		"go.mod":       "module finocode-demo\n\ngo 1.23\n",
		"main.go":      "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(Add(1, 2)) }\n",
		"calc.go":      "package main\n\n// TODO: implement Add so the tests pass.\n",
		"calc_test.go": "package main\n\nimport \"testing\"\n\nfunc TestAdd(t *testing.T) {\n\tif got := Add(2, 3); got != 5 {\n\t\tt.Fatalf(\"Add(2,3) = %d, want 5\", got)\n\t}\n}\n",
	}
}

// newWorkspace creates a temp directory and writes the seed project into it.
func newWorkspace() (*workspace, error) {
	dir, err := os.MkdirTemp("", "finocode-")
	if err != nil {
		return nil, err
	}
	w := &workspace{dir: dir, files: map[string]string{}}
	w.reset()
	return w, nil
}

// reset restores the seed project, removing any files the agent created.
func (w *workspace) reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	entries, _ := os.ReadDir(w.dir)
	for _, e := range entries {
		os.RemoveAll(filepath.Join(w.dir, e.Name()))
	}
	w.files = map[string]string{}
	for name, content := range seedFiles() {
		w.files[name] = content
		w.flush(name, content)
	}
}

// flush writes one file to the temp directory, creating parent dirs.
func (w *workspace) flush(path, content string) error {
	full := filepath.Join(w.dir, filepath.Clean(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, []byte(content), 0o644)
}

func (w *workspace) cleanup() { os.RemoveAll(w.dir) }

func (w *workspace) list() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	names := make([]string, 0, len(w.files))
	for name := range w.files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (w *workspace) read(path string) (string, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	content, ok := w.files[path]
	if !ok {
		return "", fmt.Errorf("no such file: %s", path)
	}
	return content, nil
}

// current returns the file's content or "" if it does not exist yet.
func (w *workspace) current(path string) string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.files[path]
}

func (w *workspace) write(path, content string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.files[path] = content
	return w.flush(path, content)
}

// goCommand runs "go <args>" inside the workspace and returns combined output.
// It bounds execution with goCommandTimeout so hanging model code is killed and
// reported instead of blocking the turn forever.
func (w *workspace) goCommand(parent context.Context, args ...string) string {
	ctx, cancel := context.WithTimeout(parent, goCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = w.dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=mod")
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	err := cmd.Run()
	out := strings.TrimSpace(buf.String())
	header := "$ go " + strings.Join(args, " ")
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Sprintf("%s\n%s\n(timed out after %s)", header, truncate(out, maxGoOutput), goCommandTimeout)
	}
	if err != nil {
		return fmt.Sprintf("%s\n%s\n(exit: %v)", header, truncate(out, maxGoOutput), err)
	}
	if out == "" {
		out = "(no output)"
	}
	return fmt.Sprintf("%s\n%s", header, truncate(out, maxGoOutput))
}

type emptyInput struct{}

type pathInput struct {
	Path string `json:"path" jsonschema:"description=file path relative to the project root"`
}

type writeInput struct {
	Path    string `json:"path" jsonschema:"description=file path relative to the project root"`
	Content string `json:"content" jsonschema:"description=the full new content of the file"`
}

func (w *workspace) listTool() tool.Tool {
	return must(tool.NewFunc("list_files", "List all files in the project.",
		func(ctx context.Context, _ emptyInput) (string, error) {
			return strings.Join(w.list(), "\n"), nil
		}))
}

func (w *workspace) readTool() tool.Tool {
	return must(tool.NewFunc("read_file", "Read a file's full contents.",
		func(ctx context.Context, in pathInput) (string, error) {
			return w.read(in.Path)
		}))
}

func (w *workspace) writeTool() tool.Tool {
	return must(tool.NewFunc("write_file", "Create or overwrite a file with new content.",
		func(ctx context.Context, in writeInput) (string, error) {
			if err := w.write(in.Path, in.Content); err != nil {
				return "", err
			}
			return "wrote " + in.Path, nil
		}))
}

func (w *workspace) runTestsTool() tool.Tool {
	return must(tool.NewFunc("run_tests", "Compile and run the project's Go tests (go test ./...).",
		func(ctx context.Context, _ emptyInput) (string, error) {
			return w.goCommand(ctx, "test", "./..."), nil
		}))
}

func (w *workspace) runProgramTool() tool.Tool {
	return must(tool.NewFunc("run_program", "Build and run the project's main program (go run .).",
		func(ctx context.Context, _ emptyInput) (string, error) {
			return w.goCommand(ctx, "run", "."), nil
		}))
}

func must[T any](v T, err error) T {
	if err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
	return v
}

// gjsonString extracts a top-level string field from a raw JSON object.
func gjsonString(raw json.RawMessage, key string) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) == nil {
		if s, ok := m[key].(string); ok {
			return s
		}
	}
	return ""
}

// truncate caps s to max runes (not bytes) so multi-byte characters are never
// split into invalid UTF-8 in the text fed back to the model.
func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max]) + "\n...(truncated)"
}
