# Contributing to fino

感谢你对 fino 的贡献兴趣。本文档记录编码规范和提交流程。

## 代码风格

本项目采用 [Google Go Style Guide](https://google.github.io/styleguide/go/) 作为编码规范基础。以下列出关键规则和 fino 特有的补充约束。

### 格式化与工具

- `gofmt` 是唯一格式标准。提交前必须运行 `gofmt -w .`
- 遵循 [golangci-lint](https://golangci-lint.run/) 默认规则集，核心包不应有 lint 警告

### 命名

遵循 [Google Style Guide: Naming](https://google.github.io/styleguide/go/decisions#naming)：

- 包名：小写单词，不用下划线或驼峰：`message`、`tool`、`runner`
- 公开类型、函数、常量用 PascalCase：`NewMode`、`RoleUser`、`TypeToolUse`
- 未导出标识符用 camelCase：`runHistory`、`collectTools`
- 错误变量以 `Err` 开头：`ErrMaxTurns`、`ErrToolNotFound`
- 选项函数以 `With` 开头：`WithMaxConcurrency`、`WithModelOptions`
- 接口名用简单名词：`Model`、`Tool`、`Policy`，不用 `ModelInterface` 或 `ITool`
- 缩写词全大写或全小写：`HTTP`、`JSON`、`ID`，不用 `Http`、`Json`、`Id`

### 公共 API 模式

所有公开构造函数遵循必要参数加选项：

```go
func NewMode(name, instructions string, opts ...ModeOption) (Mode, error)
func New(name string, opts ...Option) (*Agent, error)
func NewFunc[T any, R FuncReturn](name, description string, fn func(context.Context, T) (R, error), opts ...Option) (Tool, error)
```

规则：

- 必要值用显式位置参数，可选行为用 `WithXxx()`
- Option 不隐藏必要参数
- 构造函数校验输入并返回 error，不在构造时 panic
- 不提供 `MustNew` 或 `NewDefault` 变体

### 错误处理

遵循 [Google Style Guide: Errors](https://google.github.io/styleguide/go/best-practices#error-handling)：

哨兵错误用包级 `var`：

```go
var ErrMaxTurns = errors.New("max turns exceeded")
var ErrToolNotFound = errors.New("tool not found")
```

需要携带上下文的错误用自定义类型加 `Unwrap`：

```go
type ToolDeniedError struct {
    Tool     tool.Info
    Decision policy.Decision
}

func (e *ToolDeniedError) Error() string {
    return fmt.Sprintf("tool %q denied: %s", e.Tool.Name, e.Decision.Reason)
}

func (e *ToolDeniedError) Unwrap() error {
    return ErrToolDenied
}
```

错误包装用 `fmt.Errorf("%w: ...", err)`，确保 `errors.Is` 和 `errors.As` 可用。

不要用 `_` 忽略 error，除非有明确注释说明原因。

### 函数与控制流

遵循 [Google Style Guide: Functions](https://google.github.io/styleguide/go/decisions#functions)：

- 函数参数过多时考虑 option 模式，不硬编码布尔参数：`WithMaxConcurrency(2)` 优于 `New(m, 2, true, nil)`
- 早返回，减少嵌套：先处理错误和边界条件，再写主逻辑
- `if` 的初始化语句合理使用：`if err := ctx.Err(); err != nil { ... }`
- 单个函数实现不超过 50 行，超过则拆分为更小的函数

### 注释

遵循 [Google Style Guide: Comments](https://google.github.io/styleguide/go/decisions#comments)：

- 公开标识符必须有 doc comment，以标识符名称开头：`// NewMode creates a new Mode.`
- 包注释写在 `doc.go` 或对应文件顶部
- 注释描述"是什么"和"为什么"，不描述"怎么做"（那是代码的事）
- 不要重复函数签名中已有的信息

### 文件组织

- 单个源文件实现不超过 800 行，超过则按职责拆分为多个文件
- 拆分优先按职责边界划分，不为凑行数随意切割

### 依赖

核心包（message、tool、model、agent、policy、hooks、runner）只依赖标准库和 fino 内部包。不引入第三方依赖。

Provider 适配器（providers/）可以引入第三方 SDK，但不在初始核心包范围内。

### 测试

遵循 [Google Style Guide: Testing](https://google.github.io/styleguide/go/decisions#tests)：

- 测试放在同包 `_test.go` 中（白盒测试），使用包内部类型
- 可执行示例（`Example()` 函数）放在 `xxx_test` 包（黑盒测试）
- TDD 流程：先写失败测试，再实现，再验证测试通过
- 测试命名描述行为：`TestRunReturnsFinalText`，不写 `TestRunnerUnit`
- 表驱动测试优先，当测试逻辑相同只是输入不同时

## 架构约束

这些规则来自 `docs/design.md`，修改核心包前必须先读 design.md。

- **核心最小化**：不引入图引擎、RAG、MCP、托管工具或内置 session store
- **扩展点最大化**：所有外部能力必须通过接口组合，不强制特定实现
- **奥卡姆剃刀**：如果用户能用 Tool、Policy、Hook、Mode 或外层 Go 代码实现，就不加新的核心抽象
- **无隐藏状态**：Runner 只持有配置，每次运行拥有自己的消息列表
- **不绑定 provider**：provider 细节隐藏在 `model.Model` 接口之后

不应存在于核心中的路径：`graph/`、`rag/`、`session/`、`mcp/`、`tools/filesystem/`、`tools/bash/`。

## 数据模型规范

- 消息块使用扁平 discriminated union（`message.Block`），禁止 `{"text":{"text":"..."}}` 嵌套 JSON 形态
- 工具结果用 `[]message.Block`，不用纯字符串
- Handoff 通过 `HandoffTool` 接口类型断言识别，不用 metadata 字符串约定
- 工具结果批量追加为一条 `RoleTool` 消息（含多个 `tool_result` block）

## 提交流程

1. 确保所有测试通过：`go test ./...`
2. 格式化代码：`gofmt -w .`
3. 检查不应存在的路径（graph/rag/session/mcp/tools/filesystem/tools/bash/providers/）
4. 提交信息遵循 [Conventional Commits](https://www.conventionalcommits.org/)（如 `feat(tool): ...`、`docs: ...`）：用英文、祈使句，首行不超过 72 字符
5. 不自动提交，需人工确认
