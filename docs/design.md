# fino Agent SDK 设计

## 状态

本文档记录 `fino` 的初始设计。它参考了以下项目：

- `eino`：宽边界的图编排、组件和 Agent 框架
- `openai-agents-python`：Agent、Runner、Handoff、Session 设计
- `anthropic-sdk-python`：API SDK、MessageStream、ToolRunner 设计
- `anthropic-sdk-typescript`：APIPromise、MessageStream、ToolRunner、严格 SDK 边界
- `adk-go`：Google ADK 的 Agent 树、Runner、Session、工具式转移和 Go 迭代器设计

## 设计哲学

`fino` 是 Agent SDK，不是应用框架。

SDK 应该实现让 LLM Agent 可用的最小可靠循环：

```text
LLM response -> tool call -> tool execution -> tool result -> next LLM response -> final answer
```

这就是 ReAct 反馈循环：模型交替进行推理和行动，工具观察结果进入下一轮模型调用。

SDK 应把这个循环暴露为小而可组合的 Go 原语。CLI、服务端、工作流、RAG、存储、权限和部署都应由用户控制。

## 扩展性原则

`fino` 的目标不是少做功能，而是把功能放在正确边界上。

核心包必须同时满足两点：

- 不引入任何外部编排、存储、RAG 或工具库时，也能独立完成一个高质量 LLM Agent。
- 用户稍加组合或引入其他库，就能实现图编排、RAG、MCP、复杂权限、多 Agent、持久化和部署，而无需 fork 或绕开 `fino`。

因此每个核心能力都应是开放原语，而不是封闭方案：

| 能力 | 核心提供 | 用户如何扩展 |
|------|----------|--------------|
| 模型 | `model.Model` 接口 | 接入 Anthropic、OpenAI、Gemini、本地模型或代理服务 |
| 工具 | `tool.Tool` 接口和函数工具 helper | 包装文件系统、bash、MCP、RAG、浏览器、数据库或任意业务 API |
| 权限 | `policy.Policy` 接口 | 实现确认、RBAC、审计、沙箱、allowlist、风险评分 |
| 模式 | `agent.Mode` | 实现 plan/code/review/debug 等不同提示词和工具集 |
| 执行 | `runner.Run` | 外层接工作流库、HTTP handler、CLI loop、队列 worker 或 cron |
| 历史 | 显式消息输入 | 用户接入 SQLite、Redis、文件、云存储或自己的 session 系统 |
| 流式 | 语义事件 | 用户接终端 UI、WebSocket、SSE、日志系统或 tracing |
| 转移 | handoff tool helper | 用户可实现 LLM 驱动转移，也可用普通 Go 控制流实现确定性编排 |

一个能力进入核心包的条件：它必须是 ReAct 反馈循环的一部分，且不能被 Tool、Policy、Hook、Mode、Model 或 Runner 外层代码干净实现。否则它应该留在用户代码、示例或 add-on 包。

## 从参考项目学到什么

### Anthropic SDK

Anthropic 的 SDK 边界很窄：处理 API 调用、流协议、消息积累、结构化输出解析和基础工具循环，不接管应用编排。

值得学习：

- 流式输出应产生语义事件，并能还原最终消息快照。
- 工具循环可以简单且强大，不需要图引擎。
- provider 细节应隐藏在稳定的模型接口之后。

### OpenAI Agents

OpenAI 把 Agent 定义和 Runner 执行分开。Agent 定义指令、工具、模型、护栏和转移能力；Runner 执行循环并返回类型化结果与事件。

值得学习：

- Agent 定义和执行器应分离。
- 工具调用、Agent 转移和流事件应成为运行时一等事件。
- Hooks 和 Policy 应扩展行为，而不是强迫用户进入工作流引擎。

### Google ADK-Go

ADK-Go 是 Go-first 和 code-first。它使用 Agent 树和工具式 Agent 转移，而不是图编译。它不提供 DAG 编排。

值得学习：

- Go 中应偏向简单接口和显式构造函数。
- Agent 转移可以表示为普通工具。
- `iter.Seq2` 适合表达 Go 的流式事件。

### Eino

Eino 展示了边界模糊的风险：组件契约、图编译、工作流状态、checkpoint、回调层、流路由和 ADK 概念混在同一个仓库里。

关键教训：

- `fino` 不应接管图编排、字段映射、复杂 checkpoint 或 RAG 管道。

## 范围

### fino 应该做

- 定义 Agent 和 Mode。
- 执行 ReAct 反馈循环。
- 在一个模型接口之后适配不同 LLM API。
- 解析模型工具调用并执行用户工具。
- 将工具结果作为观察回注给模型。
- 支持模式级指令和工具集。
- 支持模式级默认模型选项，并允许每次运行覆盖。
- 支持工具执行前的可选策略检查。
- 支持生命周期钩子，用于可观测性和用户扩展。
- 暴露适合 CLI 和 UI 的流式事件。
- 接受显式消息历史，让用户自行掌控 session、memory 和持久化。

### fino 不应该做

- 图或 DAG 编排。
- RAG 管道、文档加载、切块、嵌入或检索。
- 内置文件系统、bash、web、搜索或代码执行工具。
- MCP 协议实现。
- HTTP 服务、CLI、worker、A2A 等部署层。
- 固定权限语义，例如 `AllowWrite` 或 `AllowNetwork`。
- 复杂图 checkpoint 或隐藏状态机。
- 在核心包里绑定 provider API 客户端细节。

## 核心概念

### Message

`message.Message` 是模型、工具和 Runner 之间交换的通用数据类型。

`fino` 使用单一 content-block 消息类型，不保留 `Message` 和 `AgenticMessage` 两条分支。

初始内容块类型：

- `text`
- `tool_use`
- `tool_result`
- `thinking`

核心不内置图片、音频或视频块。如果某个 provider 支持多模态输入，适配器可以在核心消息模型之外把用户数据翻译成 provider 请求体。

消息块使用扁平 discriminated union，而不是嵌套结构体指针。这样核心 JSON 形态更接近主流 provider，也减少 adapter 转换成本。

```go
type Block struct {
    Type      BlockType       `json:"type"`
    Text      string          `json:"text,omitempty"`
    ID        string          `json:"id,omitempty"`
    Name      string          `json:"name,omitempty"`
    Input     json.RawMessage `json:"input,omitempty"`
    ToolUseID string          `json:"tool_use_id,omitempty"`
    Content   []Block         `json:"content,omitempty"`
    IsError   bool            `json:"is_error,omitempty"`
}
```

核心 JSON 是 fino 的内部稳定格式，不承诺直接等于任意 provider wire format。Provider adapter 仍负责最终转换，但不应需要从 `{"text":{"text":"..."}}` 这类嵌套结构中反解。

### Model

`model.Model` 是 provider 抽象。

```go
type Model interface {
    Generate(ctx context.Context, messages []message.Message, tools []tool.Info, opts ...Option) (*message.Message, error)
    Stream(ctx context.Context, messages []message.Message, tools []tool.Info, opts ...Option) iter.Seq2[Event, error]
}
```

Runner 调用模型时会合并两层模型选项：先应用 Mode 默认选项，再应用本次运行的 `runner.WithModelOptions(...)`。本次运行选项优先生效。

核心包只定义接口。Provider 适配器不在初始核心包中，可以作为独立 module、独立 repo 或未来 add-on 包提供，例如：

```text
providers/anthropic
providers/openai
providers/gemini
```

### Tool

工具是普通 Go 值，包含元信息和执行方法。

```go
type Tool interface {
    Info() Info
    Run(ctx context.Context, input json.RawMessage) (Result, error)
}
```

工具结果对齐消息内容块，而不是只能返回纯字符串。

```go
type Result struct {
    Content []message.Block
    IsError bool
}
```

函数工具可以返回 `string`，SDK 会自动包装成 `text` block。更复杂的工具可以直接返回 `tool.Result`。

Go 侧使用泛型返回约束实现这一点：

```go
type FuncReturn interface {
    ~string | Result
}

func NewFunc[T any, R FuncReturn](
    name string,
    description string,
    fn func(context.Context, T) (R, error),
    opts ...Option,
) (Tool, error)
```

不提供第二套 `NewResultFunc`，避免 API 分叉。

返回值包装只识别 `FuncReturn` 约束允许的两种形态（`~string` 与 `Result`）。若未来扩展 `FuncReturn` 却漏掉对应分支，SDK 在该不可达分支返回 error 而非 panic——库代码不通过 panic 表达可恢复的内部不变量违例，调用方始终通过 `Tool.Run` 的 error 返回值感知。

函数工具使用必要参数加选项：

```go
search, err := tool.NewFunc(
    "search",
    "Search the web",
    func(ctx context.Context, input SearchInput) (string, error) {
        return searchWeb(input.Query)
    },
    tool.WithMetadata("category", "network"),
)
```

SDK 可以从 Go struct tag 推断 JSON Schema，用户也可以用选项覆盖。第一版推断只覆盖常用基础类型、slice、map 和结构体字段名；不承诺完整支持 enum、default、min/max、oneOf、递归结构和复杂嵌套校验。复杂 schema 应使用 `tool.WithSchema(...)` 显式传入。

### Mode

Mode 是可运行人格：指令、可用工具和可选元信息。

示例：

- `plan`：只读工具和规划提示词
- `code`：读写工具和编码提示词
- `review`：diff 工具和代码审查提示词

Mode 在每次运行时选择，而不是通过修改 Agent 全局状态切换。

Mode 内的工具名必须唯一。`agent.NewMode` 会在构造期检查重复工具名并返回错误，避免运行时静默覆盖。

```go
plan, err := agent.NewMode(
    "plan",
    "Plan only. Do not modify files.",
    agent.WithTools(readFile, grep, glob),
    agent.WithModelOptions(model.WithTemperature(0)),
)

result, err := runner.Run(ctx, a, runner.Text("implement this"), runner.WithMode("plan"))
```

### Agent

Agent 是与模型无关的行为配置。

```go
a, err := agent.New(
    "coder",
    agent.WithMode(plan),
    agent.WithMode(code),
    agent.WithDefaultMode("plan"),
)
```

模型由 Runner 提供。这能让 Agent 定义跨 provider 复用。

### Runner

Runner 负责执行。

```go
r, err := runner.New(
    model,
    runner.WithPolicy(policy),
    runner.WithHooks(hooks),
    runner.WithMaxTurns(20),
)

result, err := r.Run(ctx, agent, runner.Text("write a HTTP server"), runner.WithMode("code"))
```

`WithMaxTurns(20)` 是显式覆盖。Runner 默认 `MaxTurns` 是 10。一个 turn 定义为一次模型调用；同一个模型响应中的多个 tool call 仍属于同一个 turn，handoff 后目标 Agent 的下一次模型调用算下一个 turn。

为保持最小边界，Runner 不强制内置 Session。它接受显式输入，用户可以直接传一条文本，也可以传完整历史消息。

```go
result, err := r.Run(ctx, agent, runner.Text("write a HTTP server"), runner.WithMode("code"))

result, err := r.Run(ctx, agent, runner.Messages(history), runner.WithMode("code"))
```

其中 `history` 可以来自用户自己的 SQLite、Redis、文件、云存储或任何 session 库。

Runner 循环：

1. 选择 mode。
2. 从当前 mode 的指令和本次运行历史构造模型消息。
3. 调用模型。
4. 如果没有工具调用，返回最终输出。
5. 如果存在工具调用，在配置了 Policy 时先请求授权。
6. 执行匹配工具。
7. 追加工具结果。
8. 重复直到最终输出或超过最大轮次。

Runner 只维护本次运行所需的临时历史消息列表、当前 Agent 和当前 Mode，不持有隐藏全局状态。每次模型调用时，Runner 都临时构造：

```text
[system(currentMode.Instructions)] + runHistory
```

`runHistory` 永远不保存 Runner 注入的 system message。handoff 后，Runner 只更新当前 Agent 和当前 Mode；下一次模型调用会用目标 Agent 默认 Mode 的 instructions 重新拼接模型输入，而不是修改历史中的旧 system message 或追加第二条 system message。

Runner 返回值包含最终 Agent 和 Mode，方便用户在 handoff 后做恢复、审计或 UI 展示：

```go
type Result struct {
    Message   message.Message
    Messages  []message.Message
    LastAgent *agent.Agent
    LastMode  string
}
```

`context.Context` 会贯穿 Runner、Model、Policy、Hooks 和 Tool。若 `ctx` 取消，Runner 在每个 turn 开始前、模型调用前、工具调用前检查 `ctx.Err()`，并把同一个 `ctx` 传给 provider adapter 和工具函数。模型和工具内部仍应尊重自己的 `ctx`。

Runner 同时提供流式执行：

```go
events := r.Stream(ctx, agent, runner.Text("write a HTTP server"), runner.WithMode("code"))
for event, err := range events {
    if err != nil {
        return err
    }
    // render text deltas, observe tool calls, tool results, and final message
}
```

流式循环规则：

事件分层：`model.TurnMessage` 是**模型层**每个 turn 的完整 assistant 快照，由 provider 的 `model.Stream` 产生；`model.FinalMessage` 是 **Runner 层**整个 run 的终态结果，只由 `Runner.Stream` 发出一次。二者语义不重叠。

1. Runner 调用 `model.Stream` 并转发模型语义事件。
2. Provider 适配器积累流，并在每个 turn 末尾发出恰好一个 `model.TurnMessage`（不得发 `FinalMessage`，其后不得再有事件）；Runner 逐 turn 转发该快照。
3. Runner 只在收到当前 turn 的 `TurnMessage` 后解析工具调用。
4. 若没有工具调用，Runner 在该 turn 后额外发出恰好一个 `model.FinalMessage` 作为整个 run 的终态输出，然后结束迭代。
5. 若存在工具调用，Runner 对每个工具调用触发 Policy、Hooks 和工具执行。
6. Runner 发出 tool call 与 tool result 事件；handoff 在批次结束后发出。
7. Runner 将本轮所有工具结果作为一条 tool message 追加，再进入下一轮 `model.Stream`。

若 provider 违反契约（缺失 `TurnMessage`、发出第二个 `TurnMessage`、`TurnMessage` 后继续发事件、或发出 `FinalMessage`），Runner 报 `runner.ErrStreamContract`（运行期终止错误），不静默兼容。

这样 UI 可以实时渲染文本，含工具调用的中间 turn 的完整 assistant 快照也可被观测与重放，同时工具执行仍保持确定的 turn 边界。

流式模式下 Hooks 的语义与 `Run` 一致，但 `BeforeModel` / `AfterModel` 覆盖完整模型流：`BeforeModel` 在每个 turn 调用 `model.Stream` 前触发，`AfterModel` 在收到该 turn 的 `model.TurnMessage` 后触发。`BeforeTool` / `AfterTool` 在工具执行前后触发。`OnError` 对任何终止错误触发一次，然后 Runner 产出终止错误并停止迭代。

流式错误语义保持单一：任何会终止运行的错误都通过 iterator 的第二个返回值返回，并配对一个最终 `model.StreamError{Err: err}` 事件：

```go
yield(model.StreamError{Err: err}, err)
return
```

非 nil `err` 表示迭代终止，之后不会再产生事件。消费者应优先检查第二个返回值，并可直接用 `errors.Is` / `errors.As` 判断 `ErrMaxTurns`、`ErrToolNotFound`、`ToolDeniedError` 或 `context.Canceled`。`model.StreamError` 事件的用途是让事件收集器保留最终错误快照，不表示可继续执行的中间事件。

工具调用默认按模型给出的顺序串行执行。原因是串行执行最可预测，Policy 和 Hooks 的顺序稳定，也避免给用户工具引入隐式并发安全要求。

需要并行工具执行的用户可以在单个 Tool 内部自行并发，或通过显式 Runner 选项 `runner.WithMaxConcurrency(n)` opt-in。该选项只影响单个工具批次（同一模型响应中的多个 tool_use）内的执行：

- `n <= 1`（默认）：维持串行行为，无任何新增语义或并发安全要求。
- `n > 1`：单批次内最多 `n` 个工具并发执行。

并行模式保留以下确定性，把不可预测性降到最低：

- **授权仍串行**：Runner 先按调用顺序串行完成工具解析与 `policy.Authorize`（不执行用户工具代码），保证 Policy 顺序稳定，并在第一个未找到或被拒绝的工具上 fail-fast。
- **结果有序**：工具结果始终按调用顺序写回为一条 `tool` message，与完成顺序无关，符合 Anthropic / OpenAI 协议。
- **fail-fast**：首个工具错误会取消同批次其余工具的派生 `context`；Runner 按调用顺序返回第一个错误，与串行路径"首错即返回"一致。
- **Hooks ctx 作用域**：并行模式下 `BeforeTool` 返回的 `context` 只作用于该工具自身的 `Run` 与 `AfterTool`，不再线性串接到下一次模型调用；串行模式仍保持原有线性传递。
- **handoff 末位生效**：若同一批次包含多个 handoff 工具，Runner 在收集完全部结果后按调用顺序应用切换，最后一个 handoff 生效，与串行覆盖语义一致。
- **流式事件**：所有事件仍只在迭代器所在的单一 goroutine 上产生。`ToolCall` 事件按调用顺序发出，`ToolResult` 事件按完成顺序发出，`Handoff` 事件在批次结束后发出。

开启 `n > 1` 后，用户工具必须可安全并发执行。

### 消息历史与 system 指令

Mode instructions 是本次运行唯一由 Runner 负责注入的 system 指令。

`runner.Messages(history)` 中不允许包含 `system` role。若用户传入 system message，Runner 返回 `ErrSystemMessageInHistory`。这避免不同 provider 对多条 system message 的不一致处理。

需要切换系统行为时，用户应定义新的 Mode 或使用 `runner.WithMode(...)`。

### 工具结果追加策略

当模型一次返回多个 tool use 时，Runner 会先执行所有工具，再追加一条 tool message，其中包含多个 `tool_result` content block。

```text
assistant: [tool_use: call_1, tool_use: call_2]
tool:      [tool_result: call_1, tool_result: call_2]
```

这与 Anthropic 和 OpenAI 的工具调用协议更接近，也让下一轮模型调用看到完整的一批观察结果。Runner 仍会为每个工具结果单独发出流式事件，方便 UI 展示。

### Policy

Policy 是工具执行前的可选授权机制。

它不硬编码文件写入、bash、网络或其他应用语义。

```go
type Policy interface {
    Authorize(ctx context.Context, req Request) (Decision, error)
}

type Request struct {
    AgentName string
    ModeName  string
    Tool      tool.Info
    Input     json.RawMessage
}

type Decision struct {
    Allow  bool
    Reason string
}
```

用户可以用自己的 Policy 实现确认、白名单、角色检查、审计日志或拒绝规则。

Policy 返回值语义：

- `Decision{Allow:false}` 表示策略正常拒绝工具调用，Runner 返回 `ToolDeniedError`，可通过 `errors.As` 判断。
- `error != nil` 表示策略自身失败，例如远程策略服务超时。Runner 将其作为运行时错误返回，不等同于拒绝。

这样应用可以区分“安全策略拒绝”和“策略系统故障”。

### Hooks

Hooks 用于可观测性和扩展，不用于业务编排。

```go
type Hooks struct {
    BeforeModel func(context.Context, ModelCall) context.Context
    AfterModel  func(context.Context, ModelResult)
    BeforeTool  func(context.Context, ToolCall) context.Context
    AfterTool   func(context.Context, ToolResult)
    OnError     func(context.Context, error)
}
```

不提供多层 callback 系统，也不做图回调路由。

Hooks 不负责否决模型调用或工具调用。工具调用授权由 `policy.Policy` 处理。模型调用的审计、限流、缓存、重试或否决可以通过包装 `model.Model` 干净实现：

```go
type AuditedModel struct {
    Next model.Model
}

func (m AuditedModel) Generate(ctx context.Context, messages []message.Message, tools []tool.Info, opts ...model.Option) (*message.Message, error) {
    // audit or reject before delegating
    return m.Next.Generate(ctx, messages, tools, opts...)
}
```

这避免把 Policy 扩展成同时覆盖模型调用和工具调用的复杂授权系统。

`OnError` 只在进入 ReAct 循环后的运行期终止错误上触发一次：模型调用失败、工具未找到、Policy 拒绝或失败、工具执行错误、`ctx` 取消、超过 `MaxTurns`。

发生在循环开始之前的构造期与入参校验错误（`agent` 为 nil、历史包含 system message、所选 mode 不存在）不属于运行期错误，两种执行形态按各自的错误通道处理：

- `Run` 将其作为普通 `error` 直接返回，不触发 `OnError`。
- `Stream` 没有独立的 error 返回通道，统一通过 iterator 的第二个返回值并配对 `model.StreamError` 事件报告，因此这条路径也会触发 `OnError`。

这一差异由两种 API 形态决定，而非语义不一致：`Run` 用返回值表达同步错误，`Stream` 用事件流表达所有终止错误。

### Handoff

Handoff 不是独立子系统。

Agent 转移表示为普通工具 helper：

```go
handoff := agent.NewHandoffTool(targetAgent)
```

这遵循 Google ADK 的设计：转移只是模型可见的工具。用户需要 LLM 驱动的 Agent 切换时可以显式启用。

Runner 会识别 `agent.NewHandoffTool(targetAgent)` 返回的工具。该工具被调用后，Runner 会：

1. 记录 handoff 事件。
2. 追加对应 tool result，关闭当前模型工具调用。
3. 将当前 Agent 切换为目标 Agent。
4. 使用目标 Agent 的默认 mode 继续下一轮循环。

识别机制是一个小接口，而不是 metadata 字符串约定：

```go
type HandoffTool interface {
    tool.Tool
    TargetAgent() *Agent
}
```

Runner 用类型断言识别该接口。普通工具永远不会因为名字或 metadata 被误判为 handoff。

handoff 不设置单独深度限制。循环由 `MaxTurns` 兜底，因为 handoff 本质上仍是一个 turn 内的工具行为。需要更严格限制的用户可以用 Policy 拒绝特定 handoff 或在外层控制 Runner 调用。

确定性编排不需要 handoff，用户可以在外层 Go 代码中顺序调用多个 Agent。

### Streaming

Streaming 采用 Anthropic 的经验：原始 provider 事件应翻译为语义事件，并能在每个 turn 末尾生成完整的 assistant 消息快照。

事件分层（见 `docs/spec/loop-semantics.md` §5）：`TurnMessage` 是**模型层**每个 turn 的完整 assistant 快照，由 provider 的 `model.Stream` 产生；`FinalMessage` 是 **Runner 层**整个 run 的终态结果，只由 `Runner.Stream` 发出一次。provider 不得发 `FinalMessage`。

流事件类型：

- text delta
- content block start
- content block delta
- content block stop
- tool call（Runner 发出）
- tool result（Runner 发出）
- handoff（Runner 在批末发出）
- turn message（模型层：每个 turn 末尾恰好一个完整 assistant 快照）
- final message（Runner 层：整个 run 终态，仅一个）
- error

Provider 适配器负责把原生流翻译成 delta/content-block 事件，并在每个 turn 末尾发出恰好一个 `TurnMessage`（不发 `FinalMessage`，其后不再发事件）。Runner 转发这些事件，在无工具调用的最后一个 turn 之后追加唯一的 `FinalMessage`。

`content block start/delta/stop` 是保留消息快照能力的必要事件。即使第一版 provider 适配器只产生 `text delta` 和 `TurnMessage`，核心事件模型也必须保留这些事件类型。

### 错误类型

Runner 暴露可判别错误类型，方便用户用 `errors.Is` 或 `errors.As` 做条件分支。

可判别错误（与 `docs/spec/loop-semantics.md` §6 一致）：

- `ErrMaxTurns`
- `ErrToolNotFound`
- `ErrSystemMessageInHistory`
- `ErrToolDenied`（sentinel，由 `ToolDeniedError.Unwrap` 暴露）
- `ErrStreamContract`（仅 Stream：`model.Stream` 违反 `TurnMessage`/`FinalMessage` 事件契约）

`ToolDeniedError` 携带 `policy.Decision` 和工具信息，适合 UI 展示拒绝原因；用 `errors.Is(err, ErrToolDenied)` 判别，`errors.As` 取细节。`ErrStreamContract` 见 §Streaming 的事件分层。

## API 风格

所有公开构造函数使用必要参数加 options。

```go
NewX(required1, required2, opts ...Option) (*X, error)
```

规则：

- 必要值用显式位置参数传入。
- 可选行为使用 `WithXxx()`。
- Option 不隐藏必要参数。
- 构造函数校验输入并返回 error。
- API 对 `google/wire` 友好，但核心不依赖 wire。

示例：

```go
model, err := anthropic.New(
    "claude-sonnet-4-20250514",
    anthropic.WithAPIKey(apiKey),
)

readFile, err := tool.NewFunc(
    "read_file",
    "Read a UTF-8 text file",
    readFileFunc,
)

mode, err := agent.NewMode(
    "plan",
    "Plan only. Do not modify files.",
    agent.WithTools(readFile),
)

a, err := agent.New(
    "assistant",
    agent.WithMode(mode),
    agent.WithDefaultMode("plan"),
)

r, err := runner.New(model)
result, err := r.Run(ctx, a, runner.Text("inspect this project"))
```

## 初始核心包结构

```text
fino/
├── message/
│   └── message.go
├── model/
│   └── model.go
├── tool/
│   └── tool.go
├── agent/
│   └── agent.go
├── runner/
│   └── runner.go
├── policy/
│   └── policy.go
├── hooks/
│   └── hooks.go
└── doc.go
```

## 未来 Add-on

以下能力可以作为可选包或 examples，而不是核心抽象：

- 文件系统工具
- bash 工具
- grep/glob 工具
- MCP 适配器
- RAG 示例
- SQLite session store
- CLI 示例应用
- 多 Agent 示例
- 类 LangGraph 的 workflow 集成示例
- coding agent 的 policy 示例
- provider 特定结构化输出 helper

## 如何实现更大的框架能力

以下能力不进入核心，但必须能自然组合出来。

### 图编排

用户可以用任意 Go workflow/DAG 库把 `runner.Run` 作为一个节点函数。

```go
planResult, err := r.Run(ctx, planner, runner.Text(task), runner.WithMode("plan"))
codeResult, err := r.Run(ctx, coder, runner.Text(planResult.Text), runner.WithMode("code"))
```

这比在 SDK 内实现 Graph 更自由，也避免 SDK 绑定某一种图状态模型。

### RAG

RAG 可以作为工具实现：

```go
retrieve, err := tool.NewFunc("retrieve", "Retrieve relevant documents", retrieveFunc)
```

也可以由用户在调用 Runner 前自行检索，并把检索内容放进消息历史。SDK 不关心向量库、切块、重排或缓存策略。

### MCP

MCP 工具可以由外部 MCP client 枚举后适配成 `tool.Tool`。核心不实现 MCP 协议，避免重复造轮子。

### Claude Code / OpenCode / Codex 风格模式

这些应用的核心模式可以由 `Mode + Tool + Policy + Hooks` 表达：

- plan 模式：只注册只读工具，Policy 拒绝写操作。
- code 模式：注册读写工具，Policy 对高风险工具要求确认。
- review 模式：注册 diff 和 grep 工具，提示词聚焦审查。
- debug 模式：注册运行测试和日志读取工具。

SDK 不内置这些模式，但它们应当是几行用户代码即可定义的。

### 人工确认和恢复

权限确认不需要复杂 checkpoint。Policy 可以返回拒绝或特殊错误，用户在外层收集确认后重新调用 `runner.Run` 并传入同一段消息历史。

如果后续确实需要一等中断能力，也只保存待执行工具调用、当前消息历史和 mode，不引入图状态 checkpoint。

## 充分性命题

`fino` 不靠增加应用层功能取胜，而是把可靠工具型 Agent 所需的执行机制压缩进一个可规约、可测试的小内核。

命题：

> 复杂工具型 Agent 的可靠执行基础设施，不需要侵入应用的大型框架；它需要语义充分的运行时内核、显式副作用边界和可组合策略。

这条命题把 `fino` 与边界模糊的框架区分开：后者主张“我替你实现了一切”，`fino` 主张“我证明了你从不需要那个框架”。前者是产品，后者是可被检验、可被引用的论断。

命题的检验由三部分构成，互为支撑，且都不触碰核心包边界（见“非目标”与 `CLAUDE.md` 硬性约束）：

1. **形式化循环语义**：把 Runner 循环从散文升级为可机器检查的状态转移规则。
2. **循环不变量**：用 property-based 测试在任意调度下验证语义性质，而非只做逐场景断言。
3. **参考组合**：用核心之外的最小 add-on（重放、恢复、可观测、预算、评估）构造性地证明接缝足够。

这三部分分别是命题的*精确陈述*、*严格性*与*构造性证据*。当前 v0.2.x 已覆盖 ReAct 协议轨迹、流式边界、安全边界恢复和若干 `x/` 参考组合；完整的 effect-aware 审批恢复、execution tape、并发安全和幂等边界属于后续 roadmap。最初的实施计划见 `docs/superpowers/plans/2026-06-02-fino-sufficiency.md`。

## 形式化循环语义

Runner 循环是一个确定性状态转移系统。外部不确定性（模型输出、工具结果、时间、`ctx` 取消）全部作为输入注入，循环本身不持有隐藏状态。

运行状态：

```text
State = (agent, mode, history, turn)
```

`history` 永不包含 Runner 注入的 system message；每次模型调用临时构造 `[system(mode.Instructions)] + history`。

单步转移（small-step），对齐 `runner/runner.go` 实际控制流：

```text
[T-CANCEL]   ctx.Err() ≠ nil
             ⟶ OnError(err); halt(err)

[T-MODEL]    turn < maxTurns 且 ctx ok
             ⟶ msg = model.Generate(system(mode) + history)
                history' = history ++ [msg]; turn' = turn + 1

[T-FINAL]    msg.ToolUses() = ∅
             ⟶ halt(Result{Message: msg, LastAgent: agent, LastMode: mode})

[T-TOOLS]    msg.ToolUses() = [c₁ … cₙ], n ≥ 1
             ⟶ 对每个 cᵢ 按调用序: resolve → authorize → execute
                history' = history ++ [ToolResults(r₁ … rₙ)]   // 单条 RoleTool 消息
                若某 cᵢ 为 handoff: (agent', mode') = switchTo(target)，末位生效

[T-MAXTURNS] turn = maxTurns
             ⟶ OnError(ErrMaxTurns); halt(ErrMaxTurns)
```

一个 turn 定义为一次 `[T-MODEL]`。同一模型响应中的多个 tool call 属于同一 turn 的一次 `[T-TOOLS]`。handoff 后目标 agent 的下一次 `[T-MODEL]` 是新 turn。

工具批次 `[T-TOOLS]` 有两种求值策略。当前保证是 Runner 协议轨迹等价，而不是任意外部状态等价：

```text
serial   (maxConcurrency ≤ 1) : resolve→authorize→execute 按调用序逐个完成
parallel (maxConcurrency = k>1): authorize 仍按调用序串行；execute 至多 k 并发；
                                 结果按调用序写回；首错（按调用序）取消同批其余并返回
```

该等价依赖工具独立性假设。未来 `tool.Effects.ParallelSafe` 会把该假设显式化；在此之前，用户启用 `WithMaxConcurrency` 时必须保证工具并发安全。

## 循环不变量

下列性质在各自前置条件下成立，由 property-based / 状态机测试验证 Runner 可观察的协议轨迹，而非逐场景断言。

| 不变量 | 陈述 |
|--------|------|
| 结果有序 | `tool_result` 块顺序 == 对应 `tool_use` 调用顺序，与完成顺序无关 |
| 批次单消息 | 每个产生工具调用的 assistant turn 恰好追加一条 `RoleTool` 消息 |
| 终止唯一 | `Run` 恰好返回一个 Result 或一个 error；`Stream` 成功时每个模型 turn 恰好一个 `TurnMessage`、整个 run 终态恰好一个 `FinalMessage`，终止错误恰好配一个 `StreamError`（且无 `FinalMessage`） |
| OnError 一次 | 每个运行期终止错误恰好触发一次 `OnError` |
| 授权先于执行 | 任一工具的 `Run` 之前，其 `Authorize` 已返回 `Allow` |
| 协议轨迹等价 | 在工具独立性假设下，并行路径与串行路径保持结果顺序、首错选择和终止形态一致 |
| handoff 末位生效 | 同批多个 handoff 时，最终 agent/mode == 最后一个 handoff 的目标 |
| 无系统消息泄漏 | `history` 任何时刻都不含 Runner 注入的 system message |
| ctx 忠实 | `ctx` 取消后不再有新的模型调用或工具调用副作用 |
| 安全边界恢复完备 | 在 completed turn boundary 上，`history + mode` 足以继续运行；pending tool resume 是当前 opt-in 接缝，不是完整审批恢复语义 |

最后一条把安全边界恢复从核心的一个*功能*转化为核心的一个*可证明性质*。

## 接缝纪律

最小主义的堕落方向是用“你自己在外面写”逃避难题。区分“顶尖的最小”与“偷懒的最小”，只靠一条规则：

> 核心只为暴露缺失的接缝而改，永远不为实现某个能力而改。

当一个领域难题出现，决策顺序固定：

1. 先在核心之外构造它。能干净写出 ⟶ 接缝足够 ⟶ 发 add-on，核心不动。
2. 写不出，定位卡点。若因为核心未*暴露*某个本应可见的事实（如续跑所需的 pending calls 未出现在 `Result` 或事件中）⟶ 只增加*那条接缝*（一个字段或一个事件），不增加那个能力。
3. 永不把 replay / recovery / eval 的*逻辑*放进 `runner`、`agent` 等核心包。

每解决一个难题，核心要么不变，要么变得更薄而准。

## 参考组合

以下 add-on 不进入核心，作为充分性命题的构造性证据存在。它们位于独立的 `x/` 目录，只依赖标准库，且不被核心包反向导入。每个都标注它依赖的接缝、实现方式与所证明的结论。

### 重放（record & replay）

- 依赖接缝：当前记录 `model.Model` 与 `tool.Tool` 的效应。
- 实现：录制为包一层 Model/Tool 记录有序响应日志；重放为注入预录响应、旁路真实调用。
- 边界：Policy 决策、suspend/resume 决策和会影响行为的 interceptor 尚未进入完整 execution tape，因此当前 replay 只复现已记录的模型/工具轨迹，不证明完整执行等价。

```go
type RecordingModel struct{ Next model.Model; Log *Log }
type ReplayModel struct{ Log *Log } // 不调用任何真实 provider
```

### 恢复（durable continuation）

- 依赖接缝：当前不变量“安全边界恢复完备”。
- 实现：在安全边界（history 末尾为 tool 结果、user 消息或无 pending tool_use 的 assistant 文本）上序列化 `(history, mode)`；恢复即用同一段 history 继续运行。`x/recover` 的 `Snapshot` 因此只有 `History` 与 `Mode` 两个字段。
- 边界：只持久化这两样，不引入图状态 checkpoint（遵守“人工确认和恢复”一节）。批次中途（dangling `tool_use`）的 HITL 续跑由唯一的最小接缝 `runner.WithResumeFromPendingTools()`（RunOption）支持：默认关闭、行为不变；opt-in 时在首个模型 turn 前先执行 history 末尾的 pending tools。它仍只暴露接缝、不引入 checkpoint/session/graph，`x/recover` 的 `Snapshot.ResumePending` 透传该接缝。见 `docs/spec/loop-semantics.md` §7.2。
- 证明：崩溃恢复、长时运行无需核心新增中断子系统。

### 可观测性（tracing / metrics）

- 依赖接缝：`hooks.Hooks` 触发点确定 + `model.Model` 包装。
- 实现：在 Before/After 钩子里开启与结束 span，或包装 Model 记录用量。
- 证明：可观测性是横切关注点，hooks 的确定触发顺序足以承载。

### 预算（cost / token budget）

- 依赖接缝：`model.Model` 包装。
- 实现：budget-model 装饰器累计用量，超限返回 error，由 Runner 作为运行期错误终止。
- 证明：成本控制不需要核心理解 provider 计费。

### 评估（eval / 回归）

- 依赖接缝：建立在重放之上。
- 实现：固定输入 + 预录响应 ⟶ 断言最终 history 或事件序列。
- 证明：Agent 行为可复现回归测试，是重放的直接推论。

## 非目标

- 在 SDK 中复刻 Claude Code、OpenCode 或 Codex。
- 接管应用权限系统。
- 接管工作流编排。
- 接管 provider SDK。
- 接管所有可能的 LLM 使用套路。

SDK 提供循环、原语和扩展点。应用由用户掌控。
