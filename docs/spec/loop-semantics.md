# fino ReAct 循环形式化语义

> 本文是 `fino` Runner 循环的规范性参考（normative reference）。它把 `docs/design.md` 中的散文描述升级为可机器检查的状态转移系统，并定义一组对任意输入都成立的不变量。`runner/runner.go` 是本规约的参考实现；当实现与本规约冲突时，以本规约为准并修正实现。
>
> 适用范围：`runner.Run` 与 `runner.Stream`。涵盖 turn 边界、工具批次（串行与并行）、handoff、Policy 授权、Hooks 触发、`ctx` 取消与终止错误。

## 1. 记号

```text
Msg          一条 message.Message，含 Role 与 []Block
ToolUse      一个 tool_use 块: (ID, Name, Input)
ToolResult   一个 tool_result 块: (ToolUseID, Name, Content, IsError)
History      []Msg，运行期消息历史；永不含 Runner 注入的 system message
Mode         (Name, Instructions, Tools, ModelOptions)
Agent        (Name, DefaultMode, Modes)
```

辅助函数：

```text
toolUses(msg)        msg 中按出现序排列的 tool_use 块
modelInput(mode, h)  = [system(mode.Instructions)] ++ h
switchTo(agent, t)   = (t.TargetAgent(), t.TargetAgent().DefaultMode 对应的 Mode)
```

## 2. 运行状态

```text
State = (agent, mode, history, turn)
```

- `agent`、`mode`：当前活跃 Agent 与 Mode。handoff 只改这两者，不改写历史中的旧 system message，也不追加第二条 system message。
- `history`：本次运行累积的消息列表，初始为 `Input` 的拷贝。
- `turn`：已发生的模型调用次数，初始 0，上界 `maxTurns`。

不变式（结构性）：Runner 不持有跨运行的可变状态；`State` 完全由本次运行拥有。同一 `Runner` 可被多个并发运行复用。

## 3. 单步转移

转移按下列优先级匹配；每一步从当前 `State` 产生下一个 `State` 或进入终止态 `halt(·)`。

```text
[T-CANCEL]   前置: ctx.Err() ≠ nil（在每个 turn 开始前、每次工具调用前检查）
             动作: OnError(ctx, err)
             结果: halt(err)

[T-MODEL]    前置: turn < maxTurns 且 ctx.Err() = nil
             动作: BeforeModel(ctx, call)
                   msg = model.Generate(ctx, modelInput(mode, history), infos(mode.Tools), opts)
                   AfterModel(ctx, result)        // 仅当 Generate 成功
             结果: history' = history ++ [msg]
                   turn'    = turn + 1
                   失败时: OnError(ctx, err); halt(err)

[T-FINAL]    前置: 紧接 [T-MODEL] 之后且 toolUses(msg) = ∅
             结果: halt(Result{Message: msg, Messages: history,
                               LastAgent: agent, LastMode: mode.Name})

[T-TOOLS]    前置: 紧接 [T-MODEL] 之后且 toolUses(msg) = [c₁ … cₙ], n ≥ 1
             动作: 见 §4（求值策略）
             结果: history' = history ++ [ ToolResults(r₁ … rₙ) ]   // 单条 RoleTool 消息
                   若批次含 handoff: (agent', mode') = 末位 handoff 的 switchTo
                   任一步失败: OnError(ctx, err); halt(err)

[T-MAXTURNS] 前置: turn = maxTurns（循环自然退出）
             动作: OnError(ctx, wrap(ErrMaxTurns, maxTurns))
             结果: halt(ErrMaxTurns)
```

`opts` 为模型选项的两层合并：先 `mode.ModelOptions`，再本次运行 `WithModelOptions(...)`，后者追加在后，优先生效。

turn 定义：一次 `[T-MODEL]` 即一个 turn。同一 `msg` 中的多个 tool call 共属同一 turn 的一次 `[T-TOOLS]`。handoff 后目标 agent 的下一次 `[T-MODEL]` 是新 turn。handoff 不单独计深度，由 `maxTurns` 兜底。

## 4. 工具批次求值

`[T-TOOLS]` 对调用序列 `[c₁ … cₙ]` 求值，输出按调用序排列的结果块 `[r₁ … rₙ]`。单个调用的处理三段式：

```text
resolve(cᵢ)    : 在 mode.Tools 中按 Name 查找 → 找不到则 wrap(ErrToolNotFound, name)
authorize(cᵢ)  : policy.Authorize(ctx, req) →
                   err ≠ nil          ⟶ 策略系统故障，作为运行期错误返回
                   Decision.Allow=假  ⟶ ToolDeniedError{Tool, Decision}
execute(cᵢ)    : BeforeTool(ctx) → tool.Run(ctx, input) → AfterTool(ctx)   // 仅成功时 AfterTool
                 handoff 工具: 记录结果后 switchTo(target)
```

### 4.1 串行策略（maxConcurrency ≤ 1，默认）

按调用序逐个完成 `resolve → authorize → execute`，首个错误立即 `halt`。每次工具调用前检查 `ctx.Err()`。Hooks 返回的 `ctx` 线性传递到下一次调用与下一次模型调用。

### 4.2 并行策略（maxConcurrency = k > 1 且 n > 1）

```text
1. authorizeBatch: 按调用序串行完成所有 resolve + authorize（不执行用户代码）。
                   首个未找到或被拒绝的调用 fail-fast。
2. executeParallel: 至多 k 个 execute 并发；首错取消同批派生 ctx，使 ctx-aware 兄弟提前停止。
3. 收集: 结果按调用索引写回 [r₁ … rₙ]，与完成顺序无关。
4. 错误: 按调用序返回第一个非空错误（与串行“首错即返回”一致）。
5. handoff: 全部结果收集完后按调用序应用 switchTo，末位生效。
6. ctx 作用域: BeforeTool 返回的 ctx 只作用于该工具自身的 Run 与 AfterTool，不线性串接到下一次模型调用。
```

两种策略对外部可观察输出（追加的 `RoleTool` 消息、返回的错误、最终 `State`、事件序列）必须等价。差异仅限于 §4.1/§4.2 中显式声明的 ctx 作用域语义。

## 5. 流式（Stream）附加规则

`Stream` 与 `Run` 共享 §3–§4 的状态转移，附加事件语义：

事件分层（关键）：`TurnMessage` 是**模型层**每个 turn 的完整 assistant 快照，由 `model.Model.Stream` 产生；`FinalMessage` 是 **Runner 层**整个 run 的终态结果，只由 `Runner.Stream` 在最后一个无工具调用的 turn 之后发出一次。两者语义不重叠。

```text
- model.Stream 契约: 每个 turn 必须 yield 恰好一个 TurnMessage 作为终结事件，且不得 yield FinalMessage；
  TurnMessage 之后不得再有任何事件。违反者（缺失 TurnMessage、第二个 TurnMessage、TurnMessage 后继续发事件、
  任何 FinalMessage）由 Runner 报 ErrStreamContract（运行期终止错误）。
- 转发 model.Stream 的语义事件（ContentBlockStart/Delta/Stop、TextDelta）；每个 turn 的 TurnMessage 也按 turn 转发，
  使含工具调用的中间 turn 的完整 assistant 快照可观测、可重放。
- 仅在收到当前 turn 的 TurnMessage 后解析 toolUses 并执行工具。
- 每个工具调用前发 ToolCall，调用后发 ToolResult；handoff 在批次结束后发 Handoff。
- 整个 run 的最后一个 turn（无工具调用）之后，Runner 额外发出恰好一个 FinalMessage 作为 run 终态。
- 所有事件只在迭代器所在的单一 goroutine 上产生。
  ToolCall 按调用序发出；ToolResult 按完成序发出；Handoff 在批次结束后发出。
- 终止错误: yield(StreamError{Err: err}, err) 后停止迭代，且仅此一次。
- Hooks: BeforeModel 在每个 turn 调用 model.Stream 前；AfterModel 在收到该 turn TurnMessage 后；
         OnError 对任一终止错误触发一次（见 §6 与 §7 关于构造期错误的差异）。
```

## 6. 错误分类

```text
运行期终止错误（进入 ReAct 循环后）: 模型调用失败、ErrToolNotFound、Policy 拒绝(ToolDeniedError)、
                                    Policy 系统故障、工具执行错误、ctx 取消、ErrMaxTurns、
                                    ErrStreamContract（仅 Stream：model.Stream 违反事件契约）。
                                    ⟶ 触发 OnError 恰好一次。

构造期 / 入参校验错误（进入循环前）: agent 为 nil、ErrSystemMessageInHistory、所选 mode 不存在。
                                    ⟶ Run: 直接返回 error，不触发 OnError。
                                    ⟶ Stream: 经 iterator 第二返回值并配 StreamError 报告，触发 OnError。
```

可判别错误：`ErrMaxTurns`、`ErrToolNotFound`、`ErrSystemMessageInHistory`、`ErrToolDenied`（由 `ToolDeniedError.Unwrap` 暴露）、`ErrStreamContract`。消费者用 `errors.Is` / `errors.As` 分支。

## 7. 不变量

下列性质对任意模型脚本、任意工具集、任意并发调度都必须成立。`runner/invariants_test.go` 以 property-based / 状态机测试验证。

| 编号 | 名称 | 形式陈述 |
|------|------|----------|
| I1 | 结果有序 | 追加的 `ToolResults` 中第 i 个 `tool_result` 的 `ToolUseID` == 第 i 个 `tool_use` 的 `ID` |
| I2 | 批次单消息 | 每次 `[T-TOOLS]` 恰好向 history 追加一条 `RoleTool` 消息 |
| I3 | 终止唯一 | `Run` 的输出 ∈ `{一个 Result, 一个 error}`，互斥；`Stream` 成功时每个模型 turn 恰好一个 `TurnMessage`、整个 run 终态恰好一个 `FinalMessage`，终止错误恰好配一个 `StreamError`（且无 `FinalMessage`） |
| I4 | OnError 一次 | 对每个运行期终止错误，`OnError` 调用次数 == 1 |
| I5 | 授权先于执行 | 对任一被执行的 cᵢ，其 `authorize(cᵢ)` 在 `execute(cᵢ)` 之前返回 `Allow` |
| I6 | fail-fast 等价 | 固定输入下，并行返回的首个错误 == 串行返回的首个错误（按调用序） |
| I7 | handoff 末位 | 同批含 handoff `[h_j … h_m]` 时，终态 `agent` == `h_m.TargetAgent()` |
| I8 | 无系统泄漏 | ∀ 时刻，`∀ msg ∈ history: msg.Role ≠ system` |
| I9 | ctx 忠实 | `ctx` 取消后不再发生新的 `model.Generate`/`model.Stream` 或 `tool.Run` |
| I10 | 续跑完备 | `(toolUses(lastAssistant) 未完成部分, history, mode.Name)` 足以无歧义恢复运行 |

### 7.1 关于 I6（fail-fast 等价）与诱导取消

I6 要求并行批次返回的首个错误与串行（按调用序）相同。并行实现用 fail-fast 取消作为优化：首个失败者通过 `context.WithCancelCause(errSiblingFailed)` 取消兄弟工具。选首错时按调用序返回第一个**非诱导取消**的错误。

诱导取消的判定（`isInducedCancel`）：错误是 `context.Canceled`、父 `ctx` 未取消、且 batch ctx 的 cause 为 `errSiblingFailed`。这意味着 **`context.Canceled` 被解释为“工具观测到取消”而非领域错误**。在此契约下 I6 对所有不把 `context.Canceled` 当作领域返回值的工具成立。

边界（明确承认非完全串行等价）：若某低索引工具把 `context.Canceled` 当作自身领域错误返回，而同一批次中某兄弟工具并发失败先触发了取消，则该 `context.Canceled` 可能被标为诱导取消并被兄弟的真实错误取代——此时并行的首错与串行（会返回该低索引 `context.Canceled`）不一致。因此**工具不得用 `context.Canceled` 作为领域级返回值**；这是 I6 严格成立的前置契约。当低索引工具在兄弟取消之前就返回 `context.Canceled` 时，它不会被误标，I6 照常成立（见 `runner/v03_semantics_test.go`）。

### 7.2 关于 I10（续跑完备）

I10 是“恢复”能力的语义基础，但它**不要求核心实现恢复**。它要求：恢复所需的全部状态都已在 `Result`、事件或可重建的 history 中显式可见，从而恢复可在核心之外构造（见 `x/recover`）。

I10 的检验暴露了一个潜在接缝缺口，并已得出决策（探针见 `runner/recover_seam_test.go`）：

- **安全边界恢复**（history 末尾是 user/tool 消息或 assistant 文本消息）：无需任何核心改动。对持久化的 history 重新 `Run` 即可正确续跑。这是 `x/recover` 的实现契约。
- **批次中途 / 人工确认（HITL）恢复**（history 末尾是仅含 `tool_use` 而无 `tool_result` 的 dangling assistant 消息）：当前 `[T-MODEL]` 总是先调用模型，不会“先执行 history 末尾的待办工具再继续”，因此不可直接续跑。探针确认了这一行为。支持它需要新增**最小接缝**（例如 `WithResumeFromPendingTools` 这一 RunOption），按“接缝纪律”这属于*暴露接缝*而非*实现能力*；**该接缝暂不引入，留待显式签字**。在此之前，应用应在安全边界做快照，或在收集人工确认后用同一段（不含 dangling `tool_use` 的）history 重新 `Run`，由模型重新发起被批准的工具调用。

## 8. 一致性义务

任何对 `runner` 的修改必须保持 §3–§7。新增能力的正确做法是：先尝试在 `x/` 下作为参考组合构造；仅当构造暴露出缺失的接缝时，才以最小改动暴露该接缝，并在本规约登记新增的转移规则、事件或字段。
