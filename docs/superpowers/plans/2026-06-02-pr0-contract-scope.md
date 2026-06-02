# PR0 Contract Scope Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Tighten fino's public reliability claims so v0.2.x accurately describes current replay, recovery, and parallel-execution guarantees before new runtime APIs are added.

**Architecture:** This is a docs-only PR. It updates the public README, the Chinese README, `docs/design.md`, `docs/spec/loop-semantics.md`, and `CHANGELOG.md` so the contract distinguishes current safe-boundary behavior from future effect-aware suspend/resume/replay work.

**Tech Stack:** Markdown, Go test suite for regression verification, existing repository docs.

---

## File Structure

- Modify: `README.md` — English public claims and add-on table.
- Modify: `README.zh-CN.md` — Chinese public claims and add-on table.
- Modify: `docs/design.md` — design thesis, parallel semantics, replay/recover boundaries.
- Modify: `docs/spec/loop-semantics.md` — invariant names and scope notes for I6/I10.
- Modify: `CHANGELOG.md` — add an `Unreleased` note describing docs-only contract tightening.

No Go API or behavior changes belong in PR0.

### Task 0: Commit Roadmap And Plan (Prep)

**Files:**
- Add: `docs/roadmap.md` — already written.
- Add: `docs/superpowers/plans/2026-06-02-pr0-contract-scope.md` — already written.
- Modify: `README.md`, `README.zh-CN.md` — roadmap link already added to Status sections.

- [ ] **Step 1: Verify prep files are in place**

Run:

```bash
git status --short
```

Expected: `M README.md`, `M README.zh-CN.md`, `?? docs/roadmap.md`, `?? docs/superpowers/plans/2026-06-02-pr0-contract-scope.md`.

- [ ] **Step 2: Commit prep work**

```bash
git add README.md README.zh-CN.md docs/roadmap.md docs/superpowers/plans/2026-06-02-pr0-contract-scope.md
git commit -m "docs: add roadmap and PR0 contract-scope plan"
```

Expected commit subject: `docs: add roadmap and PR0 contract-scope plan`.

This commit gets the roadmap and plan onto the branch first, so the PR0 tightening commit that follows is a clean docs-only diff.

### Task 1: Tighten English README Claims

**Files:**
- Modify: `README.md:263-279`

- [ ] **Step 1: Replace the thesis paragraph**

Replace the paragraph under `## Sufficiency: hard problems without a framework` with:

```markdown
fino's thesis is that **reliable execution infrastructure for complex tool-using agents does not require an application-owning framework; it requires a semantically sufficient runtime kernel, explicit effect boundaries, and composable policies.** That claim is made checkable, not just asserted.
```

- [ ] **Step 2: Replace the three evidence bullets**

Replace the three bullets with:

```markdown
- **Precise semantics** — the ReAct loop is specified as a state-transition system in [`docs/spec/loop-semantics.md`](docs/spec/loop-semantics.md), with invariants for ordered results, single tool messages, terminal errors, stream contracts, and safe-boundary continuation.
- **Verified, not just tested** — current property tests cover the protocol trace of serial and parallel runs. Parallel claims are scoped to protocol-trace equivalence under tool-independence assumptions, not arbitrary external-state equivalence.
- **Constructive evidence** — the [`x/`](x/) packages demonstrate replay, recovery, tracing, budgets, and eval as compositions over existing seams, while documenting where future effect-aware runtime contracts are still required.
```

- [ ] **Step 3: Replace the add-on table rows for replay and recover**

Use these rows:

```markdown
| [`x/replay`](x/replay) | Reproducibility & debugging | records current model/tool effects; policy decisions and behavior-affecting interceptors are not yet part of a full execution tape |
| [`x/recover`](x/recover) | Crash recovery & durable continuation | safe-boundary continuation (`history + mode`) plus an opt-in pending-tool seam for current HITL examples |
```

- [ ] **Step 4: Keep unchanged rows for trace, budget, and eval unless wording depends on replay**

If the `x/eval` row still says `a corollary of x/replay`, replace it with:

```markdown
| [`x/eval`](x/eval) | Reproducible regression testing | currently built on recorded model/tool effects; future execution tapes will close the policy/suspend boundary |
```

- [ ] **Step 5: Verify old English overclaims are gone**

Run:

```bash
rg 'reliable complex agent capability|parallel/serial equivalence|resume-completeness|model\.Model \+ tool\.Tool are the only effect inputs' README.md
```

Expected: no matches.

### Task 2: Tighten Chinese README Claims

**Files:**
- Modify: `README.zh-CN.md:263-279`

- [ ] **Step 1: Replace the Chinese thesis paragraph**

Replace the paragraph under `## 充分性：不靠框架也能解决难题` with:

```markdown
fino 的命题是：**复杂工具型 Agent 的可靠执行基础设施，不需要侵入应用的大型框架；它需要语义充分的运行时内核、显式副作用边界和可组合策略。** 这个命题是可被检验的，而不只是口号。
```

- [ ] **Step 2: Replace the Chinese evidence bullets**

Use:

```markdown
- **精确语义**——ReAct 循环在 [`docs/spec/loop-semantics.md`](docs/spec/loop-semantics.md) 里被定义成状态转移系统，覆盖结果有序、单条工具消息、终止错误、流式契约和安全边界续跑等不变量。
- **是证明，不只是测试**——当前 property-based 测试覆盖串行与并行运行的协议轨迹。并行声明限定为“在工具独立性假设下的协议轨迹等价”，不承诺任意外部状态等价。
- **构造性证据**——[`x/`](x/) 下的包展示了 replay、recover、trace、budget、eval 如何由既有接缝组合出来，同时明确哪些边界仍需要未来的 effect-aware 运行时契约补齐。
```

- [ ] **Step 3: Replace replay/recover/eval rows**

Use:

```markdown
| [`x/replay`](x/replay) | 可复现与调试 | 记录当前的模型/工具效应；Policy 决策和会影响行为的拦截器尚未进入完整 execution tape |
| [`x/recover`](x/recover) | 崩溃恢复与续跑 | 安全边界续跑（`history + mode`），外加当前 HITL 示例使用的 opt-in pending-tool 接缝 |
| [`x/eval`](x/eval) | 可复现回归测试 | 当前基于已记录的模型/工具效应；未来 execution tape 会补齐 policy/suspend 边界 |
```

- [ ] **Step 4: Verify old Chinese overclaims are gone**

Run:

```bash
rg '可靠的复杂 Agent 能力不需要框架|续跑完备|串行与并行两种并发|唯一外部效应入口' README.zh-CN.md
```

Expected: no matches.

### Task 3: Tighten Loop Semantics Invariants

**Files:**
- Modify: `docs/spec/loop-semantics.md:146-180`

- [ ] **Step 1: Rename I6 and I10 in the invariant table**

Replace the I6 and I10 rows with:

```markdown
| I6 | 协议轨迹等价 | 在工具独立、或后续声明为可安全并行的前提下，并行执行产生与串行相同的 Runner 协议轨迹（结果顺序、首错选择、终止形态） |
| I10 | 安全边界续跑完备 | 在 completed turn boundary（history 末尾为 user/tool 消息或无 pending tool_use 的 assistant 消息）上，`(history, mode.Name)` 足以继续运行 |
```

- [ ] **Step 2: Replace section title for I6**

Replace:

```markdown
### 7.1 关于 I6（fail-fast 等价）与诱导取消
```

with:

```markdown
### 7.1 关于 I6（协议轨迹等价）与诱导取消
```

- [ ] **Step 3: Replace the first paragraph of §7.1**

Use:

```markdown
I6 当前只承诺 Runner 可观察的协议轨迹等价：工具结果按调用序写回、首错按调用序选择、终止错误路径一致。它不承诺任意外部世界状态在并行与串行之间等价；该性质需要工具彼此独立，或在未来通过 `tool.Effects.ParallelSafe` 明确声明。
```

- [ ] **Step 4: Rename section title for I10**

Replace:

```markdown
### 7.2 关于 I10（续跑完备）
```

with:

```markdown
### 7.2 关于 I10（安全边界续跑完备）
```

- [ ] **Step 5: Replace the first paragraph of §7.2**

Use:

```markdown
I10 当前只承诺安全边界续跑：当 history 已完成一个 turn 边界时，恢复所需状态由 `history + mode.Name` 表达。批次中途 / HITL 场景由 `WithResumeFromPendingTools()` 暴露最小接缝，但它不是完整 approval runtime：它不会记录 Policy 决策、不会绑定审批对象与原始输入哈希，也不提供跨进程 exactly-once 副作用保证。
```

- [ ] **Step 6: Keep existing pending-tool seam bullets but add one scope sentence**

After the existing paragraph ending with `启用接缝后执行之。`, add:

```markdown
未来若引入 `Suspend` / `ResumeApproved`，本节应拆分为安全边界恢复与一等审批恢复两套状态转移；在此之前，不得把该接缝描述为完整 HITL 恢复保证。
```

- [ ] **Step 7: Verify old invariant names are absent from the normative spec**

Run:

```bash
rg 'fail-fast 等价|续跑完备|任意模型脚本、任意工具集、任意调度' docs/spec/loop-semantics.md
```

Expected: no matches.

### Task 4: Tighten Design Document Thesis And Reference Composition Claims

**Files:**
- Modify: `docs/design.md:677-793`

- [ ] **Step 1: Replace the main thesis statement**

Replace:

```markdown
`fino` 不靠增加核心能力取胜，而是**证明现有最小核心足以可靠表达 Agent 领域的难题**。
```

with:

```markdown
`fino` 不靠增加应用层功能取胜，而是把可靠工具型 Agent 所需的执行机制压缩进一个可规约、可测试的小内核。
```

- [ ] **Step 2: Replace the quoted thesis**

Use:

```markdown
> 复杂工具型 Agent 的可靠执行基础设施，不需要侵入应用的大型框架；它需要语义充分的运行时内核、显式副作用边界和可组合策略。
```

- [ ] **Step 3: Replace the “all landed” sentence**

Replace the sentence beginning `这三部分分别是命题的*精确陈述*` with:

```markdown
这三部分分别是命题的*精确陈述*、*严格性*与*构造性证据*。当前 v0.2.x 已覆盖 ReAct 协议轨迹、流式边界、安全边界恢复和若干 `x/` 参考组合；完整的 effect-aware 审批恢复、execution tape、并发安全和幂等边界属于后续 roadmap。
```

- [ ] **Step 4: Replace the parallel equivalence paragraphs**

Replace `工具批次 [T-TOOLS] 有两种求值策略，对外部可观察结果**等价**：` with:

```markdown
工具批次 `[T-TOOLS]` 有两种求值策略。当前保证是 Runner 协议轨迹等价，而不是任意外部状态等价：
```

Replace `两条策略满足同一组不变量（下节），这正是“并行不改变语义”的精确含义。` with:

```markdown
该等价依赖工具独立性假设。未来 `tool.Effects.ParallelSafe` 会把该假设显式化；在此之前，用户启用 `WithMaxConcurrency` 时必须保证工具并发安全。
```

- [ ] **Step 5: Replace invariant rows in design.md**

In the invariant table, replace `fail-fast 等价` and `续跑完备` rows with:

```markdown
| 协议轨迹等价 | 在工具独立性假设下，并行路径与串行路径保持结果顺序、首错选择和终止形态一致 |
| 安全边界续跑完备 | 在 completed turn boundary 上，`history + mode` 足以继续运行；pending tool resume 是当前 opt-in 接缝，不是完整审批恢复语义 |
```

- [ ] **Step 6: Replace replay composition bullets**

Under `### 重放（record & replay）`, replace the three bullets with:

```markdown
- 依赖接缝：当前记录 `model.Model` 与 `tool.Tool` 的效应。
- 实现：录制为包一层 Model/Tool 记录有序响应日志；重放为注入预录响应、旁路真实调用。
- 边界：Policy 决策、suspend/resume 决策和会影响行为的 interceptor 尚未进入完整 execution tape，因此当前 replay 只复现已记录的模型/工具轨迹，不证明完整执行等价。
```

- [ ] **Step 7: Replace recovery composition bullets**

Under `### 恢复（durable continuation）`, replace the first two bullets with:

```markdown
- 依赖接缝：当前不变量“安全边界续跑完备”。
- 实现：在安全边界（history 末尾为 tool 结果、user 消息或无 pending tool_use 的 assistant 文本）上序列化 `(history, mode)`；恢复即用同一段 history 继续运行。`x/recover` 的 `Snapshot` 因此只有 `History` 与 `Mode` 两个字段。
```

- [ ] **Step 8: Verify old design overclaims are gone from normative sections**

Run:

```bash
rg '证明现有最小核心足以可靠表达|可靠的复杂 Agent 能力不需要框架|对外部可观察结果\*\*等价|并行不改变语义|一次运行的全部不确定性可被这两个接口捕获与复现|续跑完备' docs/design.md
```

Expected: no matches outside historical plan references or quoted migration notes. If matches remain in normative prose, replace them.

### Task 5: Add Changelog Entry

**Files:**
- Modify: `CHANGELOG.md:7`

- [ ] **Step 1: Insert an Unreleased section above v0.2.0**

Insert after the SemVer paragraph:

```markdown
## [Unreleased]

### Changed

- Tightened reliability claims around replay, recovery, and parallel execution:
  current guarantees are scoped to recorded model/tool effects, safe-boundary
  continuation, and protocol-trace equivalence under tool-independence
  assumptions. Full effect-aware approval/resume and execution-tape semantics
  are tracked in the roadmap rather than claimed as shipped behavior.

```

- [ ] **Step 2: Verify changelog order**

Run:

```bash
rg -n '^## ' CHANGELOG.md
```

Expected order:

```text
7:## [Unreleased]
16:## [0.2.0] - 2026-06-02
```

Line numbers can differ if wrapping changes, but `Unreleased` must appear above `0.2.0`.

### Task 6: Full Verification

**Files:**
- Verify: all files modified by PR0.

- [ ] **Step 1: Run markdown overclaim search**

Run:

```bash
rg 'resume-completeness|parallel/serial equivalence|reliable complex agent capability|model\.Model \+ tool\.Tool are the only effect inputs|可靠的复杂 Agent 能力不需要框架|续跑完备|唯一外部效应入口' README.md README.zh-CN.md docs/design.md docs/spec/loop-semantics.md CHANGELOG.md
```

Expected: no matches in normative prose. If matches remain in historical plan files outside this command, leave them alone.

- [ ] **Step 2: Run formatting check for Go impact**

Run:

```bash
gofmt -l .
```

Expected: no output. PR0 is docs-only, so this should remain clean.

- [ ] **Step 3: Run full tests**

Run:

```bash
go test ./...
```

Expected: all packages pass. PR0 should not change Go behavior, but this guards against accidental edits.

- [ ] **Step 4: Review diff**

Run:

```bash
git diff -- README.md README.zh-CN.md docs/design.md docs/spec/loop-semantics.md CHANGELOG.md
```

Expected: diff only contains wording changes described in this plan.

- [ ] **Step 5: Prepare commit after maintainer confirmation**

Do not commit without explicit maintainer confirmation. If confirmation is given, run:

```bash
git add README.md README.zh-CN.md docs/design.md docs/spec/loop-semantics.md CHANGELOG.md
git commit -m "docs: tighten execution semantics claims"
```

Expected commit subject: `docs: tighten execution semantics claims`.

## Self-Review

- Spec coverage: PR0 covers README, Chinese README, design doc, loop spec, and changelog claim tightening.
- Placeholder scan: this plan contains no placeholder tokens, deferred implementation, or unspecified file paths.
- Type consistency: no Go types are added in PR0; future type names are mentioned only as roadmap context.
- Scope check: PR0 is docs-only and does not introduce `tool.Effects`, three-state Policy, execution tapes, or runtime behavior changes.
