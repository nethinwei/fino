# fino

`fino` 是一个面向 Go 的极简 Agent SDK，核心目标是可靠实现 ReAct 反馈循环。

它只提供构建 LLM Agent 所需的最小原语：

- 模式：不同提示词、工具集和可选策略
- 模型适配：Anthropic、OpenAI、Gemini 或自定义模型
- 工具调用：解析模型输出的工具调用，执行工具，把观察结果回注给模型
- 策略钩子：可选的授权、确认、审计和拒绝逻辑
- 生命周期钩子：观测或扩展模型调用与工具执行
- 流式事件：文本增量、工具调用、工具结果和最终输出

它刻意不提供图编排、RAG 管道、托管工具、MCP 实现或部署层。用户保留完整应用控制权。

## Minimal Shape

```go
mode, err := agent.NewMode("default", "Be helpful.")
a, err := agent.New("assistant", agent.WithMode(mode), agent.WithDefaultMode("default"))
r, err := runner.New(model)
result, err := r.Run(ctx, a, runner.Text("hello"))
```

Core APIs use required arguments plus options. The SDK owns the ReAct loop and interfaces; users own orchestration, persistence, RAG, MCP, provider clients, and permission semantics.

## End-to-End

把一个真实模型、一个工具和一个 agent 串起来，复制即可运行：

```go
m, _ := deepseek.New("deepseek-v4-flash", os.Getenv("DEEPSEEK_API_KEY"))

add, _ := tool.NewFunc("add", "Add two integers", func(ctx context.Context, in addInput) (string, error) {
	return fmt.Sprintf("%d", in.A+in.B), nil
})

mode, _ := agent.NewMode("default", "Use the add tool for arithmetic.", agent.WithTools(add))
a, _ := agent.New("assistant", agent.WithMode(mode), agent.WithDefaultMode("default"))

r, _ := runner.New(m)
result, _ := r.Run(ctx, a, runner.Text("What is 2 + 3? Use the add tool."))
fmt.Println(result.Text())
```

完整可运行示例见 `examples/`：

- `examples/hello` — 最小端到端示例，带 hooks 轨迹日志
- `examples/multi_mode` — 同一 agent 在 `plan` / `code` 两个 mode 间切换
- `examples/streaming` — 消费 `Stream` 的文本增量、工具调用与工具结果事件（可见的逐字流式）
- `examples/finocode` — 交互式编码 Agent（对标 Claude Code，零依赖）：REPL 多轮对话、工具 y/N 授权 + 写文件 diff、用户驱动的 mode 切换与模型驱动的 subagent(handoff) 提示、全部 hooks，并在临时目录里用真实 `go` 工具链编译运行模型写的代码

三个示例统一用 DeepSeek 测试（`DEEPSEEK_API_KEY`，可选 `DEEPSEEK_MODEL`，默认 `deepseek-v4-flash`；`deepseek-v4-pro` 为更强档位，两档均支持思考/非思考模式）：

```bash
DEEPSEEK_API_KEY=sk-... go run ./examples/hello
```

## Providers

`providers/` 内置 7 个 provider 适配器，全部仅依赖标准库，核心保持零外部依赖：

- 通用：`providers/openai`（OpenAI 兼容）、`providers/anthropic`（Anthropic 兼容）
- 预设：`providers/deepseek`、`providers/kimi`、`providers/glm`、`providers/qwen`、`providers/minimax`

预设包在通用适配器之上封装好 base URL 与厂商特有参数；统一的扩展点是 `model.WithExtra` 与 `openai.WithExtraBody`。

设计见 `docs/design.md`。
