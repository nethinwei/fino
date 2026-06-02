# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.2.0] - 2026-06-02

### Added

- **Formal loop semantics** — `docs/spec/loop-semantics.md` specifies the ReAct
  loop as a state-transition system with ten invariants (I1–I10).
- **Property-based invariant tests** — `runner/invariants_test.go` verifies
  nine of the ten invariants (I1–I9) over many random scripts at both serial and
  parallel concurrency, including serial/parallel equivalence; the tenth (I10,
  resume-completeness) is covered separately by a seam probe
  (`runner/recover_seam_test.go`).
- **`x/` reference compositions** — `x/replay`, `x/recover`, `x/trace`,
  `x/budget`, and `x/eval`: constructive evidence that the core's seams suffice,
  each standard-library only and never imported by the core.
- **Design** — sufficiency thesis and seam discipline added to `docs/design.md`.

### Changed

- **finocode extracted** — the flagship coding-agent reference app moved out of
  `examples/` into its own repository
  ([nethinwei/finocode](https://github.com/nethinwei/finocode)) so it can grow
  its own dependencies without affecting fino's standard-library-only core.

## [0.1.0] - 2026-06-02

First tagged release. A minimal, reliable ReAct agent SDK for Go, built from
small composable primitives; the core depends on the standard library only.

### Added

- **Core ReAct loop** — `runner.Run` and `runner.Stream` with turn limits,
  pre-execution policy authorization, lifecycle hooks, clean termination, and
  opt-in bounded parallel tool execution (`runner.WithMaxConcurrency`).
- **Streaming as semantic events** — text and reasoning deltas, tool calls,
  tool results, handoffs, and a final-message snapshot over `iter.Seq2`, with a
  single consistent error path (`model.StreamError` + iterator error).
- **Agent & Mode** — one agent with multiple personas (instructions, tools,
  model options), plus model-driven handoffs modeled as ordinary tools.
- **Extension points** — `model.Model`, `tool.Tool` (+ `tool.NewFunc` with
  JSON Schema inference), `policy.Policy`, and `hooks.Hooks`.
- **Provider adapters** — `openai` and `anthropic` generic adapters plus
  `deepseek`, `kimi`, `glm`, `qwen`, and `minimax` presets, with streaming-safe
  connection timeouts and retry-with-backoff (`WithTimeout`, `WithMaxRetries`).
- **Examples** — `hello`, `multi_mode`, `streaming`, `history_trim`, and
  `finocode` (an interactive coding agent).
- **Docs** — bilingual README (English / 简体中文) and `docs/design.md`.

[0.2.0]: https://github.com/nethinwei/fino/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/nethinwei/fino/releases/tag/v0.1.0
