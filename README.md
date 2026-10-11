# Host Scout

Host Scout 是一个本地终端里的轻量交互式主机助手。v1 使用 Responses API 的 HTTP/SSE 协议，支持纯文本多轮对话、流式回答、顺序工具调用、逐次命令授权和内存内执行记录。一个用户输入启动一个 Turn；模型提出工具后继续推进，直到给出回答、澄清问题，或遇到取消、错误、显式上限。

## 启动

需要 Go 1.24 或更高版本。设置 `HOST_SCOUT_API_KEY` 后运行：

```powershell
go run ./cmd/host-scout --model <Responses 模型名>
```

若在交互式终端没有设置密钥，程序会隐藏输入读取。管道输入缺少密钥会在启动前失败。密钥不支持命令行或配置文件字段，也不会主动加入对话、记录或命令子进程环境。

默认端点为 `https://api.openai.com/v1`。`--base-url` 可以指向实现相同 `/responses`、SSE 和完整输出语义的兼容端点；不会自动降级到其他 API。非回环 HTTP 端点须显式加 `--allow-insecure-http`，带凭据、查询参数或片段的 URL 会被拒绝。

## 下载已发布的二进制

公开发行版在 [GitHub Releases](https://github.com/Chenyiax/host-scout/releases)。Linux `x86_64` 服务器可以直接下载最新的 amd64 版本：

```bash
curl -fL -o host-scout https://github.com/Chenyiax/host-scout/releases/latest/download/host-scout-linux-amd64
chmod +x host-scout
./host-scout --model gpt-6.1-sol --base-url https://api.oken.ai/v1
```

Linux `aarch64` 服务器将文件名改为 `host-scout-linux-arm64`。启动前需提供 `HOST_SCOUT_API_KEY`，交互式终端中的命令调用仍须逐次审批。下载和运行不需要在服务器上安装 Go；不要把密钥写入仓库或发行包。

## 配置

只在显式提供非空 `--config <path>` 时读取 TOML 文件。示例：

```toml
model = "<Responses 模型名>"
base_url = "https://api.openai.com/v1"
max_steps = 0
allow_insecure_http = false
```

`model` 和 `base_url` 的优先级为显式命令行参数、对应的 `HOST_SCOUT_MODEL` / `HOST_SCOUT_BASE_URL` 环境变量、配置文件、默认值；模型没有默认值。`max_steps` 缺省或为 `0` 表示每轮不限制模型调用次数，正整数设置可选护栏，显式 `--max-steps 0` 可以解除文件中的上限。`allow_insecure_http` 可被显式 `--allow-insecure-http=false` 覆盖。配置文件拒绝未知字段、错误类型、负数和溢出整数；不会搜索或创建默认文件。

一次 HTTP 重试仍只算本轮的一次模型调用。若最后允许的完整响应提出工具，程序会为该批调用记录 `skipped/step_limit`，不再执行主机操作；最后一步的纯文本回答可以正常完成。

## 交互与记录

可用本地命令：`/help`、`/status`、`/tools`、`/evidence [id]`、`/clear`、`/exit`、`/quit`。`/status` 展示实际模型、端点、平台、上限和记录数；`/tools` 展示当前发行版唯一内置工具 `run_command`。记录只保存在本进程内，`/clear` 显式清空历史和记录。模型提出普通澄清问题后，下一条输入作为新 Turn 并携带已有历史。

Ctrl+C 在就绪时清除未提交输入；在模型请求、重试等待、审批或命令运行时取消当前 Turn。命令会完成有界收尾并记录结果，然后才恢复提示符。运行中的 EOF 也会取消当前工作并退出；若收尾失败，CLI 会提示残留进程风险并以非零状态退出。管道输入中的后续行仍按顺序归属后续提示符。

历史在接受用户输入、收到完整有效模型响应、每项工具终结三个时点增量提交。部分流式文本不会作为完整回答续传，已执行的工具不会在下一轮自动重跑。明确的 `context_length_exceeded` 会显示上下文超限和 `/clear` 提示；历史与记录仍保留，程序不自动摘要、压缩、截断或缩短输入重试。普通 HTTP 400 与输出长度限制使用各自的错误分类。

## 唯一内置工具

`run_command` 接受明确的 `argv` 数组，不自动加 shell，也不提升权限。每次合法调用都会展示完整参数，只有当前授权提示下的 `y`（大小写不敏感，忽略两端空白）允许这一次；其他输入拒绝。输出会发送给模型。命令以当前用户权限运行，可能改变主机状态，且没有通用沙箱或敏感信息脱敏保证。

每次命令最多执行 10 秒，合并 stdout/stderr 最多保留 32 KiB；超出时标记截断并继续排空。子进程只继承最小环境白名单。Linux 为正式执行平台：一次已启动调用管理自己的进程组，直接进程正常或非零退出后也会终止仍在同组的普通后代；取消或超时时同样清理。收尾总预算最多 2 秒，其中管道等待最多 1 秒。终止失败、直接进程未回收或等待耗尽会返回 `failed/cleanup_failed`，保留原始 `termination_reason`、已取得输出和适用的真实退出码，且不声称残留进程已清理。主动脱离进程组的进程与已发生的副作用不在回滚保证内。Windows/macOS 是开发模式，至少清理直接进程；其进程树清理不视为 Linux 验收结果。

调用记录包含唯一 ID、CallID、工具、时间、状态、错误码和有界输出；命令另记录 argv、可空退出码、截断标识和终止原因。拒绝、失败、取消和跳过会明确区分，不伪装为成功观察。模型续接时会收到记录 ID 与结果，用户可用 `/evidence <id>` 回看。

## 模型协议与扩展入口

v1 只向基址追加 `/responses` 发起请求，使用 Bearer 认证、`stream: true`、`store: false`、`parallel_tool_calls: false` 和 function tools。完整输出中的 message、function call、reasoning 不透明载荷按原序续传，工具结果与原 `call_id` 对应。不调用 Conversations 或压缩接口，不启用自动截断。

单次模型调用只会对收到任何 SSE data 前的瞬时连接或读取失败，以及 HTTP 408、429、500、502、503、504 最多额外尝试两次。退避基准为 500 毫秒、1 秒；合法 `Retry-After` 不会被提前截短，要求超过 30 秒时停止本次重试。整个调用共用默认 5 分钟的取消与超时边界。收到任意 SSE data 后断流不会自动重发；重试只重复当前模型请求，不执行工具，也不保证远端计算恰好发生一次。

外部 Go 模块可以只导入 [`pkg/tool`](pkg/tool/tool.go)，实现 `Definition() tool.Definition` 与 `Run(context.Context, json.RawMessage) (tool.Result, error)`，再在启动时用 `tool.NewRegistry` 注册。最小示例见 [`pkg/tool/example_test.go`](pkg/tool/example_test.go)。公开入口只涵盖工具定义、状态、结果和注册，不提供完整 Agent SDK、运行中加载或动态插件系统。

## 验证与边界

本地测试使用 fake Client/Tool、Responses 模拟服务和 Linux PTY。运行 `go test ./...` 与 `go vet ./...`；Linux 取消与真终端行为需在 Linux 环境实际执行，交叉编译不等同于执行验收。逐场景证据记录在 [`docs/v1-spec-coverage.md`](docs/v1-spec-coverage.md) 和 [`docs/v1-acceptance.md`](docs/v1-acceptance.md)。

本期不提供后台命令会话、PTY 工具、stdin 续写、进程池、复杂规划、执行中消息注入、多 Agent、其他远程 API、持久化、报告下载、自动上下文压缩或 token/费用预算。
