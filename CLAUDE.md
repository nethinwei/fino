# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目概述

`fino` 是面向 Go 的极简 Agent SDK，核心目标是可靠实现 ReAct 反馈循环。它只提供构建 LLM Agent 所需的最小原语，刻意不提供图编排、RAG 管道、托管工具、MCP 实现或部署层。

## 常用命令

```bash
go test ./...                  # 运行全部测试
go test ./message              # 运行单个包的测试
go test -run TestName ./runner # 运行单个测试
gofmt -w .                     # 格式化代码
```

技术栈：Go 1.23+，核心仅依赖标准库，TDD 开发流程。

## 开发与提交流程

**`main` 受保护，禁止直接 commit/push 到 `main`（GitHub 强制 PR 模式）。** 所有改动必须走 PR：

```bash
git checkout main && git pull          # 从最新 main 起步
git checkout -b <type>/<topic>          # 例如 feat/tool-effects、docs/dev-workflow
# ... 开发并提交到该分支 ...
git push -u origin <branch>
gh pr create --base main --head <branch> --title "..." --body "..."
# CI 通过、review 后 squash 合并
```

- 如果误把提交落在本地 `main`：用 `git branch <feature>` 保存，再 `git reset --hard origin/main` 还原 `main`，然后在特性分支上推送、开 PR。
- 一个 PR 聚焦一件事；文档类改动单独开 PR，不要混进功能 PR。
- 提交信息遵循 Conventional Commits（`feat(tool): ...`、`docs: ...`）：英文、祈使句，首行不超过 72 字符（详见 `CONTRIBUTING.md`）。

**TDD 节奏（红 → 绿 → 重构）：** 先写会失败的测试并确认其失败（编译失败也算红），再写最小实现让测试转绿，最后在绿灯下重构。提交前必须 `gofmt -l .` 无输出、`go vet ./...` 与 `go test ./...` 全绿。

## 架构

核心由 7 个包组成，各自职责单一：

```
fino/
├── message/   # 角色、消息、内容块（text/tool_use/tool_result/thinking）
├── tool/      # Tool 接口、函数工具 helper、JSON Schema 推断
├── model/     # Model 接口（Generate + Stream）、流事件类型
├── agent/     # Agent 定义、Mode（指令+工具集）、Handoff tool helper
├── policy/    # Policy 接口（工具执行前授权）、AllowAll 默认实现
├── hooks/     # 生命周期钩子（BeforeModel/AfterModel/BeforeTool/AfterTool/OnError）
├── runner/    # ReAct 循环执行器、Run 输入/结果/条目
└── providers/ # Provider 适配器（anthropic/openai/deepseek 等），非核心
```

Runner 循环流程：选择 mode → 构造消息 → 调用模型 → 无工具调用则返回最终输出 → 有工具调用则经 Policy 授权 → 执行工具 → 追加结果 → 重复。

## 硬性设计约束

- **核心最小化**：不引入图引擎、RAG、MCP、托管工具或内置 session store
- **扩展点最大化**：所有外部能力必须通过 `model.Model`、`tool.Tool`、`policy.Policy`、`hooks.Hooks`、`agent.Mode` 或 `runner.Run` 输入组合
- **奥卡姆剃刀**：如果用户能用 Tool、Policy、Hook、Mode 或外层 Go 代码实现，就不加新的核心抽象
- **无隐藏状态**：Runner 只持有配置，每次运行拥有自己的消息列表
- **核心包不绑定 provider 客户端**：provider 细节隐藏在 `model.Model` 接口之后

不应存在于核心中的路径：`graph/`、`rag/`、`session/`、`mcp/`、`tools/filesystem/`、`tools/bash/`。

## API 风格

所有公开构造函数使用必要参数加 options：

```go
NewX(required1, required2, opts ...Option) (*X, error)
```

- 必要值用显式位置参数，可选行为用 `WithXxx()`
- 构造函数校验输入并返回 error（空 name、nil 函数、重复 mode、缺失 default mode 均立即报错）
- Runner 输入用 `runner.Text("...")` 或 `runner.Messages(history)`，不使用隐式 session

## 关键设计参考

设计文档 `docs/design.md` 记录了参考项目和设计决策。实现计划在 `docs/superpowers/plans/2026-06-02-fino-core-agent-sdk.md`。修改核心包前必须先读 design.md 理解边界。

## 编码规范摘要

- 遵循 [Google Go Style Guide](https://google.github.io/styleguide/go/)，`gofmt` 是唯一格式标准
- 公开 API 一律 `NewX(required, opts ...Option) (*X, error)`，不使用 `NewXDefault` 或 `MustNew`
- Error 变量用 `var ErrXxx = errors.New(...)`，可携带上下文的错误用 `type XxxError struct{ ... }` 加 `Unwrap() error`
- 不引入外部依赖，核心包只依赖标准库
- 测试放在同包 `_test.go` 中，可执行示例放在 `xxx_test` 包（`package xxx_test`）
- 扁平 `Block` discriminated union，禁止 `{"text":{"text":"..."}}` 嵌套 JSON 形态
- Handoff 通过 `HandoffTool` 接口类型断言识别，不用 metadata 字符串约定
- 工具结果批量追加为一条 `RoleTool` 消息
- 单个函数实现不超过 50 行，单个源文件不超过 800 行，超过则拆分
- 完整规范见 `CONTRIBUTING.md`
