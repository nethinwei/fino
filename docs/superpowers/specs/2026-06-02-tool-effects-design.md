# PR1 Design: Typed Tool Effects

## Goal

Add a typed `tool.Effects` struct so that tool authors can declare the effect
profile of their tools at registration time. The declaration is visible to
`policy.Policy` via the existing `policy.Request.Tool` field (which is already
`tool.Info`). PR1 is declaration-only: the Runner does not change its behavior
based on Effects.

## Motivation

Without typed effects, the runtime cannot distinguish a read-only lookup from a
destructive file-delete. Policy implementations must rely on tool names or
untyped metadata, which is brittle and ad-hoc. Future PRs (PR2 three-state
policy, PR5 effect-aware concurrency) depend on a shared effect vocabulary being
available on every tool.

## Design

### New type: `tool.Effects`

```go
// Effects declares the effect profile of a tool. The zero value means
// "unspecified" — the runtime MUST treat unspecified as conservative (not safe).
// A field being false does NOT mean the tool lacks that property; it means the
// tool has not declared it.
type Effects struct {
	ReadOnly         bool // Tool performs no external writes.
	Idempotent       bool // Repeated calls with the same input have no additional effect.
	ParallelSafe     bool // Safe to run concurrently with other ParallelSafe tools.
	Destructive      bool // Tool may irreversibly destroy data or resources.
	ExternalWrite    bool // Tool writes to external systems (APIs, filesystems, DBs).
	RequiresApproval bool // Tool should be gated by human approval before execution.
	SensitiveInput   bool // Tool input may contain secrets or PII.
	SensitiveOutput  bool // Tool output may contain secrets or PII.
}
```

**Zero-value semantics:** Unspecified means conservative. The Runner (in future
PRs) must NOT infer that a tool is safe just because `ParallelSafe == false`. It
means the tool has not declared itself safe.

### Modified type: `tool.Info`

```go
type Info struct {
	Name        string
	Description string
	InputSchema json.RawMessage
	Metadata    map[string]any
	Effects     Effects // NEW — zero value = unspecified/conservative
}
```

Adding a field to `Info` is backward-compatible: existing code that constructs
`Info` without setting `Effects` gets the zero value (unspecified), which is the
correct conservative default.

### New option: `tool.WithEffects`

```go
// WithEffects sets the effect declaration for a function tool.
func WithEffects(e Effects) Option {
	return func(c *funcConfig) { c.effects = e }
}
```

The `funcConfig` struct gains an `effects Effects` field. `NewFunc` copies it
into the resulting `Info.Effects`.

### Policy visibility

`policy.Request.Tool` is already `tool.Info`. After PR1, any Policy
implementation can inspect `req.Tool.Effects` without any changes to
`policy.Policy` or `policy.Request`.

### What does NOT change

| Component | Status |
| --- | --- |
| `Runner` behavior | No change. Does not read Effects. |
| `Runner` concurrency | No change. `WithMaxConcurrency` ignores Effects. |
| Default `AllowAll` policy | No change. Still allows everything. |
| `model.Model` / providers | No change. Effects are not sent to models. |
| Handoff tools | Default zero-value Effects. |
| `x/replay`, `x/recover`, etc. | No change. Effects are not recorded/replayed. |

### Backward compatibility

- `tool.Info` gains one exported field. Existing struct literals that don't name
  all fields will still compile (Go allows partial struct literals for exported
  fields). Code that uses `Info{Name: ..., Description: ..., InputSchema: ...}`
  is unaffected.
- `tool.NewFunc` gains one new `Option`. Existing calls without `WithEffects`
  produce tools with zero-value Effects (unspecified/conservative).
- `policy.Request` is unchanged.

### Testing strategy

1. **Unit test in `tool/`:** Verify that `WithEffects` populates `Info().Effects`
   correctly, and that omitting `WithEffects` yields zero-value Effects.
2. **Integration test in `runner/`:** Verify that `policy.Request.Tool.Effects`
   received by a recording Policy matches what the tool declared.
3. **Property test extension:** Add an Effects field to the prop-test tool
   generator; verify that the invariant tests still pass (effects are inert in
   PR1).

### File changes

| File | Change |
| --- | --- |
| `tool/tool.go` | Add `Effects` struct, add `Effects` field to `Info`, add `WithEffects` option, wire into `NewFunc`. |
| `tool/tool_test.go` | Test `WithEffects` and zero-value default. |
| `runner/runner_audit_test.go` | Test that Policy sees `Effects` in `Request.Tool`. |
| `runner/model_state_test.go` | Add Effects to prop-test tool generator. |
| `docs/roadmap.md` | Mark PR1 as in-progress or completed. |

### Non-goals for PR1

- Runner does not use Effects to gate concurrency (that is PR5).
- Runner does not use RequiresApproval to auto-deny (that is PR2/PR3).
- No CLI flag or config for default effects.
- No effects inference from function signatures.
- No effects composition (combining effects of multiple tools in a batch).

## Self-Review

- **Placeholder scan:** None found.
- **Internal consistency:** `Info.Effects` is the single source of truth;
  `WithEffects` is the only way to set it on function tools; `policy.Request`
  already carries `tool.Info`.
- **Scope check:** Focused on one struct, one field, one option, two test files.
  Suitable for a single implementation plan.
- **Ambiguity check:** Zero-value semantics explicitly documented as
  "unspecified/conservative". No dual interpretation possible.
