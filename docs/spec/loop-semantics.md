# fino ReAct 循环形式化语义

> 本文是 `fino` Runner 循环的规范性参考（normative reference）。它把 `docs/design.md` 中的散文描述升级为可机器检查的状态转移系统，并定义一组对任意输入都成立的不变量。`runner/runner.go` 是本规约的参考实现；当实现与本规约冲突时，以本规约为准并修正实现。
>
> 适用范围：`runner.Run`、`runner.Stream` 与 `runner.ResumeApproved`。涵盖 turn 边界、工具批次（串行与并行）、handoff、Policy 授权、Hooks 触发、`ctx` 取消、挂起与审批恢复、终止错误。

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
             步骤1 authorizeBatch: 按调用序对每个 cᵢ 做 resolve + authorize（不执行任何工具）
                   - resolve 失败              ⟶ OnError; halt(wrap(ErrToolNotFound, name))
                   - ResolvedKind = Deny       ⟶ OnError; halt(ToolDeniedError{Tool, Decision})（fail-fast）
                   - ResolvedKind = Suspend    ⟶ 收集进 pending 列表，继续后续调用（不短路，
                                                  使靠后的 Deny 仍优先于靠前的 Suspend）
             步骤2 suspend 检查: pending 非空 ⟶ [T-SUSPEND]（不执行任何工具）
             步骤3 executeBatch: 见 §4（求值策略），执行全部已授权调用
             结果: history' = history ++ [ ToolResults(r₁ … rₙ) ]   // 单条 RoleTool 消息
                   若批次含 handoff: (agent', mode') = 末位 handoff 的 switchTo
                   执行期任一步失败: OnError(ctx, err); halt(err)

[T-SUSPEND]  前置: authorizeBatch 收集到 ≥1 个 Suspend 且无 Deny
             动作: 无（不执行任何工具，包括同批被 Allow 的调用）
             结果: halt(Result{ Suspended: true,
                               PendingCalls: [{Tool, Call, Reason} for 每个 Suspend 调用]（仅 Suspend，
                                              被 Allow 但未执行的调用不计入）,
                               Messages: history（末尾为含全部 tool_use 的 dangling assistant 消息）,
                               LastAgent: agent, LastMode: mode.Name })
             说明: 非错误路径，不触发 OnError。仅 Run 支持；Stream 将 Suspend 降级为 Deny（见 §5）。

[T-MAXTURNS] 前置: turn = maxTurns（循环自然退出）
             动作: OnError(ctx, wrap(ErrMaxTurns, maxTurns))
             结果: halt(ErrMaxTurns)
```

`ResumeApproved` 是 `Run` 之外的另一个循环入口：它从一个挂起 `Result` 的快照 `SuspendedRun` 出发，对该批挂起调用执行人工审批，再续接 `[T-MODEL]` 循环。它不是正常单步优先级的一部分，单列如下：

```text
[T-RESUME]   前置: ResumeApproved(ctx, a, suspended, approvals, opts) 被调用
             步骤0 agent 校验: a.Name() == suspended.LastAgentName
                   不匹配 ⟶ 返回 ErrResumeAgentMismatch（循环前；不触发 OnError）
             步骤1 校验 SuspendedRun + approvals:
                   - suspended.Messages 不含 system message（否则 ErrSystemMessageInHistory，护 I8）
                   - suspended.Messages 末尾须为含 ≥1 个 tool_use 的 dangling assistant
                   - PendingCalls 非空（空 pending 会退化为无审批盲恢复，拒绝）
                   - 每个 PendingCall.Call.ID 须出现在该批 tool_use 中，且不重复
                   - approvals 对每个 PendingCall 恰好一个，无未知 CallID、无重复
                   校验失败 ⟶ 返回 ApprovalError / wrap(ErrInvalidApproval)（循环前；不触发 OnError）
             步骤1b mode 锁定: opts 不得 override mode（cfg.modeName 必须 == suspended.LastMode），
                   否则该批 tool_use 会在错误工具集中 resolve ⟶ wrap(ErrInvalidApproval)。其余 opts（如模型选项）照常生效。
             步骤2 executeBatch（不重新授权; 见 §4 求值策略，始终串行按调用序）:
                   对末批每个 tool_use cᵢ:
                     若 cᵢ ∈ PendingCalls 且其 approval.Approved=false:
                        产出 tool_result{IsError:true, "rejected: <Reason>"}（不执行工具）
                     否则（已批准的挂起调用，或同批被 Allow 未执行的调用）:
                        resolve + execute（执行错误 ⟶ OnError; halt(err)）
             步骤3 追加: history' = suspended.Messages ++ [ ToolResults(r₁ … rₙ) ]（单条 RoleTool）
                   若批含 handoff（被批准执行的 handoff 工具）: 末位 switchTo 生效
             步骤4 续接 [T-MODEL] 循环
```

`ResumeApproved` 不调用 `Policy.Authorize`：挂起调用已在原批 `authorizeBatch` 中得到 `Suspend`/`Allow` 决策，人工审批替代策略授权做最终决定。续接循环本身可再次挂起（产出新的挂起 `Result`）。

`opts` 为模型选项的两层合并：先 `mode.ModelOptions`，再本次运行 `WithModelOptions(...)`，后者追加在后，优先生效。

turn 定义：一次 `[T-MODEL]` 即一个 turn。同一 `msg` 中的多个 tool call 共属同一 turn 的一次 `[T-TOOLS]`。handoff 后目标 agent 的下一次 `[T-MODEL]` 是新 turn。handoff 不单独计深度，由 `maxTurns` 兜底。

## 4. 工具批次求值

`[T-TOOLS]` 对调用序列 `[c₁ … cₙ]` 求值，输出按调用序排列的结果块 `[r₁ … rₙ]`。单个调用的处理三段式：

授权与执行分两阶段（见 [T-TOOLS]）：先对整批 `authorizeBatch`，再 `executeBatch`。单个调用的处理：

```text
resolve(cᵢ)    : 在 mode.Tools 中按 Name 查找 → 找不到则 wrap(ErrToolNotFound, name)
authorize(cᵢ)  : policy.Authorize(ctx, req) →
                   err ≠ nil                 ⟶ 策略系统故障，作为运行期错误返回
                   ResolvedKind = Deny       ⟶ ToolDeniedError{Tool, Decision}
                   ResolvedKind = Suspend    ⟶ 收集进 pending（批末若有 pending 则 [T-SUSPEND]）
                   ResolvedKind = Allow      ⟶ 进入 executeBatch
execute(cᵢ)    : BeforeTool(ctx) → tool.Run(ctx, input) → AfterTool(ctx)   // 仅成功时 AfterTool
                 handoff 工具: 记录结果后 switchTo(target)
```

`ResolvedKind` 见 §6 与 `policy.Decision.ResolvedKind()`：未设 `Kind` 时回退到旧的 `Allow` 布尔（真→Allow，假→Deny），保证既有二态 Policy 不变。

### 4.1 串行策略（maxConcurrency ≤ 1，默认）

先对整批按调用序完成 `resolve → authorize`（authorize-all-before-execute），任一 Deny 或 Suspend 都使该批**无任何工具执行**；随后按调用序逐个 `execute`，首个执行错误立即 `halt`。每次工具调用前检查 `ctx.Err()`。Hooks 返回的 `ctx` 线性传递到下一次调用与下一次模型调用。

> 行为收紧：旧串行实现是逐调用 `authorize→execute`，靠后调用被 Deny 前靠前调用已产生副作用。改为整批先授权后，`call₁=Allow + call₂=Deny` 时 call₁ 不再执行——这使串/并行在 fail-fast 下真正等价（I6），符合“批内任一被拒则全批无副作用”的网关语义。

### 4.2 并行策略（maxConcurrency = k > 1 且 n > 1）

```text
1. authorizeBatch: 按调用序串行完成所有 resolve + authorize（不执行用户代码）。
                   首个未找到或被拒绝的调用 fail-fast；任一 Suspend 收集进 pending，
                   批末 pending 非空则 [T-SUSPEND]，全批不执行。
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
- Suspend 降级: Stream 没有 suspended Result 出口（返回 iter.Seq2[Event, error]），且 FinalMessage 只用于无工具调用的终态 turn。
  故 Stream 将 DecisionSuspend 降级为 ToolDeniedError（运行期终止错误），不引入新事件类型。完整 suspend 语义仅在 Run。
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

构造期 / 入参校验错误（进入循环前）: agent 为 nil、ErrSystemMessageInHistory、所选 mode 不存在；
                                    ResumeApproved 的 ErrResumeAgentMismatch、ErrSystemMessageInHistory，
                                    以及 wrap(ErrInvalidApproval)（ApprovalError，或快照非法：空 pending / mode override / dangling 缺失）。
                                    ⟶ Run / ResumeApproved: 直接返回 error，不触发 OnError，不执行任何工具。
                                    ⟶ Stream: 经 iterator 第二返回值并配 StreamError 报告，触发 OnError。

非错误终止（Run）: DecisionSuspend 经 [T-SUSPEND] 产出 Result{Suspended:true}，不是错误，
                  不触发 OnError，不配 StreamError。Stream 不支持该路径（降级为 ToolDeniedError）。
```

`ResumeApproved` 在校验通过后进入 executeBatch；该阶段的工具执行错误属运行期终止错误，触发 OnError 恰好一次（与 `[T-TOOLS]` 一致）。

可判别错误：`ErrMaxTurns`、`ErrToolNotFound`、`ErrSystemMessageInHistory`、`ErrToolDenied`（由 `ToolDeniedError.Unwrap` 暴露）、`ErrStreamContract`、`ErrNotSuspended`（`Result.SuspendedRun` 用）、`ErrInvalidApproval`（由 `ApprovalError.Unwrap` 暴露）、`ErrResumeAgentMismatch`。`ErrNotSuspended` 与 `ErrInvalidApproval` 是不同条件，不得混淆。消费者用 `errors.Is` / `errors.As` 分支。

## 7. 不变量

下列性质必须在各自前置条件下成立。`runner/invariants_test.go` 以 property-based / 状态机测试验证 Runner 可观察的协议轨迹。

| 编号 | 名称 | 形式陈述 |
|------|------|----------|
| I1 | 结果有序 | 追加的 `ToolResults` 中第 i 个 `tool_result` 的 `ToolUseID` == 第 i 个 `tool_use` 的 `ID` |
| I2 | 批次单消息 | 每次 `[T-TOOLS]` 恰好向 history 追加一条 `RoleTool` 消息 |
| I3 | 终止唯一 | `Run`（及 `ResumeApproved`）的输出 ∈ `{一个完成 Result(Suspended=false), 一个挂起 Result(Suspended=true), 一个 error}`，三者互斥；`Stream` 成功时每个模型 turn 恰好一个 `TurnMessage`、整个 run 终态恰好一个 `FinalMessage`，终止错误恰好配一个 `StreamError`（且无 `FinalMessage`） |
| I4 | OnError 一次 | 对每个运行期终止错误，`OnError` 调用次数 == 1；挂起（非错误）不触发 OnError |
| I5 | 授权先于执行 | 对任一被执行的 cᵢ，其 `authorize(cᵢ)` 在 `execute(cᵢ)` 之前返回 `ResolvedKind = Allow` |
| I11 | 挂起精确性 | `[T-SUSPEND]` 后 `PendingCalls` 恰好等于该批 `ResolvedKind = Suspend` 的调用（按调用序）；被 Allow 但未执行的调用不计入，且该批不追加任何 `RoleTool` 消息 |
| I6 | 协议轨迹等价 | 在工具独立、或后续声明为可安全并行的前提下，并行执行产生与串行相同的 Runner 协议轨迹（结果顺序、首错选择、终止形态） |
| I7 | handoff 末位 | 同批含 handoff `[h_j … h_m]` 时，终态 `agent` == `h_m.TargetAgent()` |
| I8 | 无系统泄漏 | ∀ 时刻，`∀ msg ∈ history: msg.Role ≠ system` |
| I9 | ctx 忠实 | `ctx` 取消后不再发生新的 `model.Generate`/`model.Stream` 或 `tool.Run` |
| I10 | 安全边界恢复完备 | 在 completed turn boundary（history 末尾为 user/tool 消息或无 pending tool_use 的 assistant 消息）上，`(history, mode.Name)` 足以继续运行 |
| I12 | 审批恢复完备 | `ResumeApproved` 校验通过后，挂起批中**每个** `tool_use` 在追加的单条 `RoleTool` 消息中都有对应 `tool_result`（被批准/原 Allow 的为真实结果，被拒绝的为 `IsError=true` 的 `rejected:` 结果），故续接的 `[T-MODEL]` 观察到一个良构 history |

### 7.1 关于 I6（协议轨迹等价）与诱导取消

I6 当前只承诺 Runner 可观察的协议轨迹等价：工具结果按调用序写回、首错按调用序选择、终止错误路径一致。它不承诺任意外部世界状态在并行与串行之间等价；该性质需要工具彼此独立，或在未来通过 `tool.Effects.ParallelSafe` 明确声明。

诱导取消的判定（`isInducedCancel`）：错误是 `context.Canceled`、父 `ctx` 未取消、且 batch ctx 的 cause 为 `errSiblingFailed`。这意味着 **`context.Canceled` 被解释为“工具观测到取消”而非领域错误**。在此契约下 I6 对所有不把 `context.Canceled` 当作领域返回值的工具成立。

边界（明确承认非完全串行等价）：若某低索引工具把 `context.Canceled` 当作自身领域错误返回，而同一批次中某兄弟工具并发失败先触发了取消，则该 `context.Canceled` 可能被标为诱导取消并被兄弟的真实错误取代——此时并行的首错与串行（会返回该低索引 `context.Canceled`）不一致。因此**工具不得用 `context.Canceled` 作为领域级返回值**；这是 I6 严格成立的前置契约。当低索引工具在兄弟取消之前就返回 `context.Canceled` 时，它不会被误标，I6 照常成立（见 `runner/v03_semantics_test.go`）。

### 7.2 关于 I10（安全边界恢复完备）

I10 当前只承诺安全边界续跑：当 history 已完成一个 turn 边界时，恢复所需状态由 `history + mode.Name` 表达。批次中途 / HITL 场景由 `WithResumeFromPendingTools()` 暴露最小接缝，但它不是完整 approval runtime：它不会记录 Policy 决策、不会绑定审批对象与原始输入哈希，也不提供跨进程 exactly-once 副作用保证。

I10 的检验暴露了一个潜在接缝缺口，并已得出决策（探针见 `runner/recover_seam_test.go`）：

- **安全边界恢复**（history 末尾是 user/tool 消息或 assistant 文本消息）：无需任何核心改动。对持久化的 history 重新 `Run` 即可正确续跑。这是 `x/recover` 的实现契约。
- **批次中途 / 人工确认（HITL）恢复**（history 末尾是含至少一个 `tool_use`、且其后无对应 `tool_result` 的 dangling assistant 消息）：默认 `[T-MODEL]` 总是先调用模型，不会“先执行 history 末尾的待办工具再继续”，因此默认不可直接续跑。为此暴露**唯一的最小接缝** `WithResumeFromPendingTools()`（RunOption）：

  - **检测**：pending = 末条消息为 `RoleAssistant` 且含 ≥1 个 `tool_use` 时该消息的全部 `tool_use`；不要求该 assistant 消息仅含 `tool_use`（允许与 thinking/text 混排）。否则无 pending。
  - **转移**：启用该接缝且存在 pending 时，在进入循环前先对 pending 执行一次 `[T-TOOLS]`（授权 → 执行 → 追加单条 `RoleTool` 消息；`Stream` 同时发出 `ToolCall`/`ToolResult`/批末 `Handoff`），随后照常进入 `[T-MODEL]`。关闭（默认）或无 pending 时为 no-op，行为与未启用时完全一致。
  - **纪律**：这是*暴露接缝*而非*实现能力*——不引入 checkpoint / session / graph 概念，恢复所需状态仍只是 history + mode。`x/recover` 的 `Snapshot.Resume` 可经 `runner.WithResumeFromPendingTools()` 透传以覆盖该边界；推荐直接用便捷入口 `Snapshot.ResumePending`，它已内置该接缝。

  探针 `runner/recover_seam_test.go` 同时固定两侧行为：默认不自动执行 dangling 工具；启用接缝后执行之。

`WithResumeFromPendingTools()` 是**盲恢复**：它无条件执行全部 pending 工具，不带审批，适用于崩溃恢复（`x/recover`）。需要逐调用人工裁决时用 §7.3 的 `ResumeApproved`，二者并存且互不内嵌。

### 7.3 关于 I12（审批恢复完备）

`[T-SUSPEND]` 产出的挂起 `Result` 经 `Result.SuspendedRun()` 提取为值快照 `SuspendedRun{Messages, LastAgentName, LastMode, PendingCalls}`（纯数据，不含活对象引用），由调用方自行持久化，再连同活的 agent 一起传入 `ResumeApproved`（见 §3 `[T-RESUME]`）。这是一等审批恢复，与 §7.2 的安全边界恢复是两套不同的转移：

- **agent 上下文**：挂起前可能已发生 handoff，活跃 agent 不再是根 agent。`SuspendedRun` 只记录 `LastAgentName`/`LastMode`（纯数据，不持有活对象引用）；活 agent 是调用方重建的代码，由其显式传入。能否 JSON 序列化取决于 `PendingCall` 内 `tool.Info.Metadata` 是否可 marshal，核心不清洗。`ResumeApproved` 校验传入 agent 的 `Name()` 与 `LastAgentName` 一致，避免在错误 agent 上下文中 resolve 工具。`PendingToolCall` 不重复携带 per-call agent/mode（同批必属单一 agent+mode）。
- **审批裁决**：`Approval{CallID, Approved, Reason}` 按 `CallID` 绑定到挂起调用。批准→执行工具；拒绝→写入 `IsError=true` 的 `rejected: <Reason>` 结果，使模型可见并据此调整（拒绝是模型可见的结果，不是隐藏控制流）。
- **不重新授权**：挂起调用已被授权，`ResumeApproved` 不再调用 `Policy.Authorize`（D5）。
- **纪律**：这是*暴露能力*的最小一等 API，仍不引入 checkpoint/session/graph；快照只是 `history + agent/mode + 挂起调用`。`InputHash`/防篡改、跨进程 exactly-once、Stream 的 suspend/resume 事件均不在核心内（属 `x/` 或应用层）。

`x/recover` 的 `Snapshot` 仅承载安全边界恢复（I10）；审批恢复由 `runner.ResumeApproved` 一等承载，二者职责不重叠。

## 8. 一致性义务

任何对 `runner` 的修改必须保持 §3–§7。新增能力的正确做法是：先尝试在 `x/` 下作为参考组合构造；仅当构造暴露出缺失的接缝时，才以最小改动暴露该接缝，并在本规约登记新增的转移规则、事件或字段。
