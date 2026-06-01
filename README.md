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

设计见 `docs/design.md`。
