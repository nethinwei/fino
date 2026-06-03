# fino

**面向 Go 的极简 ReAct Agent SDK：只给小而能拼的原语，不做框架。**

[English](README.md) | **简体中文**

[![CI](https://github.com/nethinwei/fino/actions/workflows/ci.yml/badge.svg)](https://github.com/nethinwei/fino/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/nethinwei/fino.svg)](https://pkg.go.dev/github.com/nethinwei/fino)
[![Go Report Card](https://goreportcard.com/badge/github.com/nethinwei/fino)](https://goreportcard.com/report/github.com/nethinwei/fino)
[![Go](https://img.shields.io/badge/go-1.23%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/dl/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)
[![Std-lib only](https://img.shields.io/badge/dependencies-stdlib%20only-success)](go.mod)

fino 只干一件事，并且尽量干好：把让 LLM Agent 真正能用起来的 **ReAct 反馈循环**跑通。

```text
模型响应 → 工具调用 → 工具执行 → 工具结果 → 下一轮模型响应 → 最终答案
```

剩下的事情（编排、持久化、RAG、MCP、权限、部署）一概不碰，全留给上层自己实现，藏在几个一下午就能写完的小接口后面。没有图引擎，没有隐藏状态，也不绑定任何厂商。核心只依赖标准库。

---

## 为什么是 fino

很多 Agent 框架做着做着，就把你的整个应用给接管了。fino 偏不。它故意把边界划得很窄，每个能力都做成一个开放的原语，而不是一个封装死的功能。

| 想要什么 | fino 给的原语 | 仍然由你说了算 |
| --- | --- | --- |
| 模型 | `model.Model` 接口 | 用哪个 LLM、走代理还是本地跑 |
| 工具 | `tool.Tool` + `tool.NewFunc` | 文件系统、bash、MCP、RAG、数据库、任意业务 API |
| 授权 | `policy.Policy` 接口 | 确认、RBAC、审计、沙箱、白名单 |
| 人格 | `agent.Mode` | plan / code / review / debug 各自的提示词和工具集 |
| 可观测 | `hooks.Hooks` | 日志、tracing、指标、成本统计 |
| 多 Agent | handoff 工具 | LLM 自己决定转移，或者用普通 Go 控制流写死流程 |
| 记忆和历史 | 显式传消息 | SQLite、Redis、文件，或自家的 session 系统 |
| 执行 | `runner.Run` / `runner.Stream` | HTTP handler、CLI、队列、cron、工作流 |

一个能力想进核心，得同时满足两条：它是 ReAct 循环的一部分，而且没法靠 Tool、Policy、Hook、Mode、Model 或 Runner 外面的代码干净地实现。否则它就该待在业务代码、示例或扩展包里，进不来。

## 能做什么

- **把 ReAct 循环做对**：轮数上限、工具授权、生命周期钩子，以及干净的终止逻辑。
- **流式就是语义事件**：文本增量、实时思考、工具调用、工具结果、转移，每个模型 turn 一份完整 assistant 快照（`TurnMessage`），以及整个 run 一份终态事件——完成时 `FinalMessage`、Policy 挂起待审批时 `Suspended`，统统走 `iter.Seq2`。
- **Mode（模式）**：一个 agent 挂多副人格，各有各的指令、工具和模型参数。
- **Handoff（转移）**：模型驱动的 agent 间切换，本质上就是一个普通工具。
- **Policy 随你换**：每次工具执行前都能放行、拒绝或拦下来。
- **Hooks 看得见**：观测、扩展整个循环，又不用去改它。
- **并行工具有上界**：单批工具调用里可选并发执行，结果顺序照旧确定。
- **传输够韧**：内置的 provider 带流式安全的连接超时和退避重试。
- **核心零依赖**：除了标准库，啥都不要。

## 安装

```bash
go get github.com/nethinwei/fino
```

需要 Go 1.23 以上（用到了 `iter.Seq2`）。

## 上手

一个模型、一个工具、一个 agent，下面这段拷过去就能跑：

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

> 上面为了短，用 `_` 把错误吞了。实际每个构造函数都会返回 error，[`examples/`](examples/) 里的程序都老老实实处理了。

## 几个核心包

七个包，各管一摊：

```text
message/  角色、消息、内容块（text / tool_use / tool_result / thinking）
tool/     Tool 接口、函数工具 helper、JSON Schema 推断
model/    Model 接口（Generate + Stream）、流事件类型
agent/    Agent、Mode（指令 + 工具）、handoff 工具 helper
policy/   Policy 接口（执行前授权）、AllowAll 默认实现
hooks/    生命周期钩子（BeforeModel / AfterModel / BeforeTool / AfterTool / OnError）
runner/   ReAct 循环执行器：Run、Stream、Input、Result
```

Runner 本身只存配置，每次运行各自拿一份消息列表，所以一个 Runner 拿去并发复用也没问题。

## 流式

`Stream` 吐出来的是语义事件，可以直接接终端 UI、WebSocket 或 trace。

```go
for ev, err := range r.Stream(ctx, a, runner.Text(prompt)) {
	if err != nil {
		log.Fatal(err) // 终止错误，迭代到此为止
	}
	switch e := ev.(type) {
	case model.TextDelta:
		fmt.Print(e.Text) // 一个 token 一个 token 地来
	case model.ContentBlockDelta:
		// 实时的思考（thinking）片段
	case model.ToolCall:
		fmt.Printf("\n→ %s(%s)\n", e.Call.Name, e.Call.Input)
	case model.ToolResult:
		fmt.Printf("← %s\n", e.Result.Text())
	case model.Handoff:
		fmt.Printf("⇄ 转给 %s\n", e.Target)
	case model.TurnMessage:
		// 每个模型 turn 的完整 assistant 快照
	case model.FinalMessage:
		// 完成时的 run 终态结果（由 Runner 发出，仅一次）
	case model.Suspended:
		// Policy 挂起批次待人工审批；重建 SuspendedRun 后恢复：
		//   sr := runner.SuspendedRunFrom(e)
		//   r.ResumeApproved(ctx, a, sr, approvals)
	}
}
```

凡是会让运行终止的错误，都从迭代器第二个返回值抛出来，同时再配一个 `model.StreamError` 事件兜底——错误就这一条路。判断类型直接用 `errors.Is` / `errors.As` 比对 `ErrMaxTurns`、`ErrToolNotFound`、`ToolDeniedError` 或 `context.Canceled`。

## 工具

工具就是任何实现了 `tool.Tool` 的类型。`NewFunc` 这个 helper 能把一个带类型的 Go 函数直接变成工具，顺手从 struct tag 里把 JSON Schema 推出来：

```go
search, err := tool.NewFunc(
	"search", "Search the web",
	func(ctx context.Context, in SearchInput) (string, error) {
		return searchWeb(in.Query)
	},
	tool.WithMetadata("category", "network"),
)
```

返回 `string` 会自动包成 text block；要输出结构化、多块的内容就返回 `tool.Result`。想自己写 schema，传个 `tool.WithSchema(...)` 就行。

## 用 Policy 管授权

每次工具调用之前，都会先问一遍 Policy。确认、RBAC、沙箱、风险评分，想怎么管都行——核心只内置了一个 `AllowAll`。

```go
type confirmPolicy struct{}

func (confirmPolicy) Authorize(ctx context.Context, req policy.Request) (policy.Decision, error) {
	if req.Tool.Name == "delete_file" {
		return policy.Decision{Allow: false, Reason: "破坏性操作得先审一下"}, nil
	}
	return policy.Decision{Allow: true}, nil
}

r, _ := runner.New(m, runner.WithPolicy(confirmPolicy{}))
```

被拒的调用会拿到一个 `*runner.ToolDeniedError`；要是返回了 error，那是 Policy 系统自己出毛病了——这两种情况是分开看的。

## 用 Hooks 做观测

Hooks 只在旁边观测、扩展，不动循环本身。字段全都可以不填（nil-safe）。

```go
r, _ := runner.New(m, runner.WithHooks(&hooks.Hooks{
	BeforeModel: func(ctx context.Context, c hooks.ModelCall) context.Context {
		log.Printf("→ 调模型 (%s/%s)，%d 条消息", c.AgentName, c.ModeName, len(c.Messages))
		return ctx
	},
	AfterTool: func(ctx context.Context, r hooks.ToolResult) {
		log.Printf("← 工具 %s 跑完了", r.Tool.Name)
	},
	OnError: func(ctx context.Context, err error) { log.Printf("出错: %v", err) },
}))
```

## Mode 和多 Agent 转移

一个 agent 可以挂好几个 mode（人格）；一次运行从哪个 mode 起步随你定，模型也能通过 handoff 工具切到别的 agent。

```go
plan, _ := agent.NewMode("plan", "Think and outline. Do not edit files.")
code, _ := agent.NewMode("code", "Implement the plan.", agent.WithTools(editFile))
a, _ := agent.New("assistant", agent.WithMode(plan), agent.WithMode(code), agent.WithDefaultMode("plan"))

result, _ := r.Run(ctx, a, runner.Text("加一个 /health 接口"), runner.WithMode("code"))

// 把活儿交给专门的 agent，本质就是一个普通工具：
handoff, _ := agent.NewHandoffTool(reviewer)
```

## Providers（适配器）

`providers/` 里内置了 7 个适配器，全都只用标准库，核心因此能保持零外部依赖：

- **通用**：`providers/openai`（OpenAI 兼容）、`providers/anthropic`（Anthropic 兼容）
- **预设**：`providers/deepseek`、`providers/kimi`、`providers/glm`、`providers/qwen`、`providers/minimax`

预设包就是在通用适配器外面，把各家的 base URL 和特有参数提前封好。通用的扩展口子是 `model.WithExtra` 和 `openai.WithExtraBody`。适配器自带流式安全的连接超时和退避重试：

```go
m, _ := openai.New("gpt-4o",
	openai.WithAPIKey(os.Getenv("OPENAI_API_KEY")),
	openai.WithTimeout(30*time.Second), // 只管 dial + TLS，不会掐断流
	openai.WithMaxRetries(2),           // 碰到 429 / 5xx 指数退避
)
```

想自己接一个 provider？实现 `model.Model`（`Generate` + `Stream`）就够了。

## 示例

| 示例 | 看点 |
| --- | --- |
| [`examples/hello`](examples/hello) | 最小的端到端跑通，带一条 hook 轨迹日志 |
| [`examples/multi_mode`](examples/multi_mode) | 同一个 agent 在 `plan` / `code` 之间来回切 |
| [`examples/streaming`](examples/streaming) | 消费 `Stream` 事件，逐字流式肉眼可见 |
| [`examples/history_trim`](examples/history_trim) | 包装 `model.Model` 裁剪历史——一个包装器通吃所有 provider 的组合范式 |
| [`examples/cookbook`](examples/cookbook) | 离线、确定性的难题配方——HITL 审批 + 续跑、有界并行工具、RAG 即工具——外加 MCP 即工具的纯文字指引 |
| [**finocode**](https://github.com/nethinwei/finocode) ↗ | 旗舰参考应用，已独立成仓：对标 Claude Code、只基于 fino 搭建的编码 Agent——REPL 多轮、工具 y/N 授权 + 写文件 diff、mode 切换、handoff 子 agent、全套 hooks，还在临时目录里用真的 `go` 工具链编译运行模型写的代码。充分性命题的构造性证据。 |

对接 provider 的示例默认走 DeepSeek（[`examples/cookbook`](examples/cookbook) 用内嵌 scripted model，离线即可运行，无需 API key）：

```bash
DEEPSEEK_API_KEY=sk-... go run ./examples/streaming
# 可选：DEEPSEEK_MODEL=deepseek-v4-pro（更强的档位；两档都支持思考模式）
```

## fino 不做什么

有些东西 fino 是故意不做的：图/DAG 编排、RAG 管道（加载、切块、嵌入、检索）、内置的文件系统/bash/web/搜索/代码执行工具、MCP 协议、HTTP 服务和 CLI/worker、写死的权限语义（比如 `AllowWrite`）、藏在背后的 session store 或状态机。

这些放在业务代码、示例或扩展包里更合适——跟 fino 搭着用，而不是焊死在它身上。

## 充分性：不靠框架也能解决难题

fino 的命题是：**复杂工具型 Agent 的可靠执行基础设施，不需要侵入应用的大型框架；它需要语义充分的运行时内核、显式副作用边界和可组合策略。** 这个命题是可被检验的，而不只是口号。

- **精确语义**——ReAct 循环在 [`docs/spec/loop-semantics.md`](docs/spec/loop-semantics.md) 里被定义成状态转移系统，覆盖结果有序、单条工具消息、终止错误、流式契约和安全边界续跑等不变量。
- **是证明，不只是测试**——当前 property-based 测试覆盖串行与并行运行的协议轨迹。并行声明限定为“在工具独立性假设下的协议轨迹等价”，不承诺任意外部状态等价。
- **构造性证据**——[`x/`](x/) 下的包展示了 replay、recover、trace、budget、eval 如何由既有接缝组合出来，同时明确哪些边界仍需要未来的 effect-aware 运行时契约补齐。

| Add-on | 难题 | 依赖的接缝 |
| --- | --- | --- |
| [`x/replay`](x/replay) | 可复现与审计 | 在公共接缝上记录 execution tape——模型响应、Policy 决策、工具执行、suspend、approval、resume 与终止；重放时不调用真实 provider、工具或 Policy |
| [`x/recover`](x/recover) | 崩溃恢复与续跑 | 安全边界续跑（`history + mode`）及盲恢复的 opt-in pending-tool 接缝；HITL 审批恢复由 `runner.ResumeApproved` 承载，不经 `x/recover` |
| [`x/trace`](x/trace) | tracing 与可观测性 | `hooks.Hooks` 的确定触发顺序 |
| [`x/budget`](x/budget) | 成本 / token 预算 | `model.Model` 装饰器 |
| [`x/eval`](x/eval) | 可复现回归测试 | 在已记录的 tape 上跑确定性用例；`RunWithOptions` 可为依赖 Policy 的 fixture 接入 `ReplayPolicy` |

replay tape 是可复现性与审计证据，不证明业务正确性；它不提供 exactly-once 副作用、durable workflow 或防篡改能力。

核心永远不为“增加能力”而改，只在确有必要时为“暴露缺失接缝”而改。详见 [`docs/design.md`](docs/design.md) 的**接缝纪律**。

## 状态

API 风格是统一的（`NewX(required, opts ...Option) (*X, error)`），整体在往稳定走，但打出 `v1` tag 之前还可能改。要保证可复现，先 pin 到某个 commit。

effect-aware concurrency（`WithMaxConcurrency` 受 `Effects.ParallelSafe` 把关）与幂等边界（`tool.ExecutionContext` + `WithRunID`）已落地。后续到参考证明案例的演进路径见 [`docs/roadmap.md`](docs/roadmap.md)。

## 参与进来

欢迎提 Issue 和 PR。动核心包之前，先扫一眼 [`CONTRIBUTING.md`](CONTRIBUTING.md)（编码规范：Google Go Style、`gofmt`、TDD、核心不引外部依赖）和 [`docs/design.md`](docs/design.md)（设计边界）。

## 许可证

[MIT](LICENSE) © nethinwei
