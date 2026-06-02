# fino

**A minimal, reliable ReAct agent SDK for Go — built from small composable primitives, not a framework.**

**English** | [简体中文](README.zh-CN.md)

[![CI](https://github.com/nethinwei/fino/actions/workflows/ci.yml/badge.svg)](https://github.com/nethinwei/fino/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/nethinwei/fino.svg)](https://pkg.go.dev/github.com/nethinwei/fino)
[![Go Report Card](https://goreportcard.com/badge/github.com/nethinwei/fino)](https://goreportcard.com/report/github.com/nethinwei/fino)
[![Go](https://img.shields.io/badge/go-1.23%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/dl/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Std-lib only](https://img.shields.io/badge/dependencies-stdlib%20only-success)](go.mod)

`fino` gives you exactly one thing, done well: the **ReAct feedback loop** that makes an LLM agent work.

```text
model response → tool call → tool execution → tool result → next model response → final answer
```

Everything else — orchestration, persistence, RAG, MCP, permissions, deployment — stays in *your* code, behind small interfaces you can implement in an afternoon. No graph engine. No hidden state. No vendor lock-in. The core depends on **the Go standard library only**.

---

## Why fino?

Most agent frameworks grow until they own your application. `fino` does the opposite: it draws a deliberately narrow boundary and exposes every capability as an **open primitive** instead of a closed feature.

| You want… | fino gives you a primitive | You stay in control of |
| --- | --- | --- |
| A model | `model.Model` interface | Which LLM, proxy, or local runtime |
| A tool | `tool.Tool` + `tool.NewFunc` | Filesystem, bash, MCP, RAG, DB, any API |
| Authorization | `policy.Policy` interface | Confirmation, RBAC, audit, sandbox, allowlists |
| Personas | `agent.Mode` | plan / code / review / debug instructions & toolsets |
| Observability | `hooks.Hooks` | Logging, tracing, metrics, cost accounting |
| Multi-agent | handoff tool helper | LLM-driven or deterministic Go control flow |
| Memory & history | explicit message input | SQLite, Redis, files, your own session store |
| Execution | `runner.Run` / `runner.Stream` | HTTP handlers, CLI loops, queues, cron, workflows |

A capability earns a place in the core only if it is part of the ReAct loop **and** cannot be implemented cleanly via a Tool, Policy, Hook, Mode, Model, or code around the Runner. Otherwise it belongs in your code — not ours.

## Features

- **ReAct loop, done right** — turn limits, tool authorization, lifecycle hooks, and clean termination.
- **Streaming as first-class semantic events** — text deltas, live reasoning, tool calls, tool results, handoffs, and a final-message snapshot via `iter.Seq2`.
- **Modes** — one agent, multiple personas (distinct instructions, tools, and model options).
- **Handoffs** — model-driven transfer between agents, modeled as an ordinary tool.
- **Pluggable policy** — authorize, deny, or gate every tool call before it runs.
- **Lifecycle hooks** — observe and extend model calls and tool executions without forking the loop.
- **Bounded parallel tools** — opt-in concurrent execution within a single tool-call batch, with deterministic ordering.
- **Production-grade transport** — streaming-safe connection timeouts and retry-with-backoff in the bundled providers.
- **Zero core dependencies** — the standard library, nothing else.

## Install

```bash
go get github.com/nethinwei/fino
```

Requires Go 1.23+ (for `iter.Seq2`).

## Quickstart

A model, a tool, and an agent — copy-paste runnable:

```go
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/nethinwei/fino/agent"
	"github.com/nethinwei/fino/providers/deepseek"
	"github.com/nethinwei/fino/runner"
	"github.com/nethinwei/fino/tool"
)

type addInput struct {
	A int `json:"a" jsonschema:"description=first addend"`
	B int `json:"b" jsonschema:"description=second addend"`
}

func main() {
	m, _ := deepseek.New("deepseek-v4-flash", os.Getenv("DEEPSEEK_API_KEY"))

	add, _ := tool.NewFunc("add", "Add two integers",
		func(ctx context.Context, in addInput) (string, error) {
			return fmt.Sprintf("%d", in.A+in.B), nil
		})

	mode, _ := agent.NewMode("default", "Use the add tool for arithmetic.", agent.WithTools(add))
	a, _ := agent.New("assistant", agent.WithMode(mode), agent.WithDefaultMode("default"))

	r, _ := runner.New(m)
	result, _ := r.Run(context.Background(), a, runner.Text("What is 2 + 3? Use the add tool."))
	fmt.Println(result.Text())
}
```

```bash
DEEPSEEK_API_KEY=sk-... go run .
```

> Errors are elided with `_` for brevity. All constructors return errors and the runnable programs in [`examples/`](examples/) handle them properly.

## Core concepts

Seven single-purpose packages:

```text
message/  roles, messages, content blocks (text / tool_use / tool_result / thinking)
tool/     Tool interface, function-tool helper, JSON Schema inference
model/    Model interface (Generate + Stream), stream event types
agent/    Agent, Mode (instructions + tools), handoff tool helper
policy/   Policy interface (pre-execution authorization), AllowAll default
hooks/    lifecycle hooks (BeforeModel / AfterModel / BeforeTool / AfterTool / OnError)
runner/   the ReAct loop executor — Run, Stream, Input, Result
```

The Runner holds only configuration; **each run owns its own message list**, so one Runner is safe to reuse across concurrent runs.

## Streaming

`Stream` yields semantic events you can pipe straight into a terminal UI, WebSocket, or trace.

```go
for ev, err := range r.Stream(ctx, a, runner.Text(prompt)) {
	if err != nil {
		log.Fatal(err) // terminal error; iteration stops
	}
	switch e := ev.(type) {
	case model.TextDelta:
		fmt.Print(e.Text) // token-by-token
	case model.ContentBlockDelta:
		// live reasoning ("thinking") fragments
	case model.ToolCall:
		fmt.Printf("\n→ %s(%s)\n", e.Call.Name, e.Call.Input)
	case model.ToolResult:
		fmt.Printf("← %s\n", e.Result.Text())
	case model.Handoff:
		fmt.Printf("⇄ handoff to %s\n", e.Target)
	case model.TurnMessage:
		// complete assistant snapshot for each model turn
	case model.FinalMessage:
		// run-terminal result (emitted once, by the Runner)
	}
}
```

All terminal errors are reported through the iterator's second return value **and** paired with a final `model.StreamError` event — one consistent error path. Use `errors.Is` / `errors.As` for `ErrMaxTurns`, `ErrToolNotFound`, `ToolDeniedError`, or `context.Canceled`.

## Tools

A tool is any type implementing `tool.Tool`. The `NewFunc` helper turns a typed Go function into one and infers the JSON Schema from struct tags:

```go
search, err := tool.NewFunc(
	"search", "Search the web",
	func(ctx context.Context, in SearchInput) (string, error) {
		return searchWeb(in.Query)
	},
	tool.WithMetadata("category", "network"),
)
```

Return `string` (auto-wrapped as a text block) or a `tool.Result` for structured/multi-block output. Need a hand-written schema? Pass `tool.WithSchema(...)`.

## Authorization with Policy

A `Policy` is consulted before **every** tool call. Implement confirmation, RBAC, sandboxing, or risk scoring — the core ships only `AllowAll`.

```go
type confirmPolicy struct{}

func (confirmPolicy) Authorize(ctx context.Context, req policy.Request) (policy.Decision, error) {
	if req.Tool.Name == "delete_file" {
		return policy.Decision{Allow: false, Reason: "destructive op needs review"}, nil
	}
	return policy.Decision{Allow: true}, nil
}

r, _ := runner.New(m, runner.WithPolicy(confirmPolicy{}))
```

A denied call surfaces as a `*runner.ToolDeniedError`; a returned error means the policy system itself failed — the two are distinct by design.

## Observability with Hooks

Hooks observe and extend the loop without changing it. All fields are nil-safe.

```go
r, _ := runner.New(m, runner.WithHooks(&hooks.Hooks{
	BeforeModel: func(ctx context.Context, c hooks.ModelCall) context.Context {
		log.Printf("→ model (%s/%s), %d msgs", c.AgentName, c.ModeName, len(c.Messages))
		return ctx
	},
	AfterTool: func(ctx context.Context, r hooks.ToolResult) {
		log.Printf("← tool %s", r.Tool.Name)
	},
	OnError: func(ctx context.Context, err error) { log.Printf("error: %v", err) },
}))
```

## Modes & multi-agent handoff

One agent can hold several modes (personas); a run can start in any of them, and the model can switch agents through a handoff tool.

```go
plan, _ := agent.NewMode("plan", "Think and outline. Do not edit files.")
code, _ := agent.NewMode("code", "Implement the plan.", agent.WithTools(editFile))
a, _ := agent.New("assistant", agent.WithMode(plan), agent.WithMode(code), agent.WithDefaultMode("plan"))

result, _ := r.Run(ctx, a, runner.Text("Add a /health endpoint"), runner.WithMode("code"))

// Hand control to a specialist agent — modeled as a plain tool:
handoff, _ := agent.NewHandoffTool(reviewer)
```

## Providers

`providers/` ships seven adapters, all standard-library only, so the core stays dependency-free:

- **Generic:** `providers/openai` (OpenAI-compatible), `providers/anthropic` (Anthropic-compatible)
- **Presets:** `providers/deepseek`, `providers/kimi`, `providers/glm`, `providers/qwen`, `providers/minimax`

Presets wrap the generic adapters with the right base URL and vendor-specific parameters. The universal extension points are `model.WithExtra` and `openai.WithExtraBody`. Adapters include streaming-safe connection timeouts and retry-with-backoff:

```go
m, _ := openai.New("gpt-4o",
	openai.WithAPIKey(os.Getenv("OPENAI_API_KEY")),
	openai.WithTimeout(30*time.Second), // bounds dial + TLS, never the stream
	openai.WithMaxRetries(2),           // exponential backoff on 429 / 5xx
)
```

Implementing your own provider is just satisfying `model.Model` (`Generate` + `Stream`).

## Examples

| Example | What it shows |
| --- | --- |
| [`examples/hello`](examples/hello) | Minimal end-to-end run with a hook trace log |
| [`examples/multi_mode`](examples/multi_mode) | One agent switching between `plan` and `code` modes |
| [`examples/streaming`](examples/streaming) | Consuming `Stream` events with visible token-by-token output |
| [`examples/history_trim`](examples/history_trim) | Wrapping `model.Model` to trim history — the composition pattern, one wrapper for all providers |
| [**finocode**](https://github.com/nethinwei/finocode) ↗ | The flagship reference app, in its own repo: a Claude Code-style coding agent built only on fino — REPL, y/N tool authorization with write diffs, mode switching, handoff sub-agents, full hooks, and a real `go` toolchain in a temp workspace. A constructive proof of the sufficiency thesis. |

All examples run against DeepSeek by default:

```bash
DEEPSEEK_API_KEY=sk-... go run ./examples/streaming
# optional: DEEPSEEK_MODEL=deepseek-v4-pro (stronger tier; both support thinking modes)
```

## What fino is *not*

By design, fino does **not** include: graph/DAG orchestration · RAG pipelines (loaders, chunking, embeddings, retrieval) · built-in filesystem/bash/web/search/code tools · an MCP implementation · HTTP servers, CLIs, or workers · fixed permission semantics (e.g. `AllowWrite`) · hidden session stores or state machines.

Each of these is better expressed in your code, an example, or an add-on package — composed *with* fino, never bolted *into* it.

## Sufficiency: hard problems without a framework

fino's thesis is that **reliable complex agent capability needs no framework — only correct minimal primitives, precise semantics, and composition.** That claim is made checkable, not just asserted:

- **Precise semantics** — the ReAct loop is specified as a state-transition system in [`docs/spec/loop-semantics.md`](docs/spec/loop-semantics.md), with ten invariants (ordered results, single tool message, OnError-exactly-once, parallel/serial equivalence, …).
- **Verified, not just tested** — nine of the ten are checked by property-based tests over a thousand-plus random scripts at both serial and parallel concurrency (`runner/invariants_test.go`); the tenth (resume-completeness) is covered by a dedicated seam probe (`runner/recover_seam_test.go`).
- **Constructive evidence** — the [`x/`](x/) packages solve the field's hard problems entirely outside the core, each as a thin composition over the existing seams:

| Add-on | Problem | Seam it rides on |
| --- | --- | --- |
| [`x/replay`](x/replay) | Reproducibility & debugging | `model.Model` + `tool.Tool` are the only effect inputs |
| [`x/recover`](x/recover) | Crash recovery & durable continuation | the *resume-completeness* invariant (serialize only history + mode) |
| [`x/trace`](x/trace) | Tracing & observability | deterministic `hooks.Hooks` firing |
| [`x/budget`](x/budget) | Cost / token budgets | a `model.Model` decorator |
| [`x/eval`](x/eval) | Reproducible regression testing | a corollary of `x/replay` |

The core never changes to add a capability — only, if ever, to expose a missing seam. See the **seam discipline** in [`docs/design.md`](docs/design.md).

## Status

The API follows a consistent shape — `NewX(required, opts ...Option) (*X, error)` — and is approaching stability, but may still change before a tagged `v1`. Pin a commit if you need reproducibility.

## Contributing

Issues and PRs are welcome. Please read [`CONTRIBUTING.md`](CONTRIBUTING.md) for the coding standards (Google Go Style, `gofmt`, TDD, no external core dependencies) and [`docs/design.md`](docs/design.md) for the design boundaries before changing core packages.

## License

[MIT](LICENSE) © nethinwei
