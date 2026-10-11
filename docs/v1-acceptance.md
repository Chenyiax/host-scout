# v1 MVP 验收记录

变更：`v1-interactive-agent-mvp`。日期：2026-10-11（Asia/Shanghai）。逐场景测试映射见 [v1 Spec 场景覆盖](v1-spec-coverage.md)。自动化测试使用本地 Responses 模拟服务和测试密钥；另用用户提供的第三方端点完成了有限的在线冒烟验证，见下文。

## 执行环境与总体结果

| 检查 | 本次结果 |
| --- | --- |
| Windows amd64，Go 1.26.2：`go test ./... -count=1` | 通过；所有有测试的包均通过 |
| Windows amd64：`go vet ./...` | 通过 |
| `GOOS=linux/darwin/windows GOARCH=amd64 go build ./...` | 三个平台编译通过 |
| Linux amd64：实际执行 command、CLI、Agent、Responses Provider 测试二进制 | 四组均通过 |
| Linux controlling PTY：`TestLinuxPTY*` | 对话、授权、取消、EOF、可选上限和恢复场景均通过 |
| OpenSpec 严格规格校验 | 见本文最后的校验记录 |
| 第三方 Responses 兼容端点在线冒烟 | `gpt-6.1-sol` 的 CLI 纯文本轮次、工具提议、拒绝结果续传与记录查询通过；获批后的真实命令执行未测 |

Linux 实测环境是 WSL2 `docker-desktop`，内核 `6.18.33.2-microsoft-standard-WSL2`。本机 Windows Go 交叉编译出 Linux 测试二进制后，在 WSL2 **实际运行**；因此进程组、取消和 PTY 结论来自 Linux 执行结果。macOS 只验证编译，Windows 运行了单元与非终端 CLI 测试；两者的真终端行为未实测。交叉编译结果不作为 Linux 运行验收证据。

## Linux 真终端逐项验收

PTY 测试打开 `/dev/ptmx`，启动独立 CLI 子进程并设置 controlling terminal，向真实终端输入字符、Ctrl+C（`0x03`）和 Ctrl+D（`0x04`）。模型服务只监听测试回环地址，检查请求历史、工具定义和调用次数。

| 场景 | 测试 | 结果 |
| --- | --- | --- |
| 就绪时 Ctrl+C 清除未提交输入，流式回答及下一轮目标纠正 | `TestLinuxPTYInteractiveMVP` | 通过；未提交文本没有进入模型 |
| 同批两条命令分别批准、拒绝，反馈模型；`/evidence` 查询两条记录 | `TestLinuxPTYInteractiveMVP` | 通过；只批准一次的 argv 被执行 |
| 模型流中 Ctrl+C、授权中 Ctrl+C，下一轮继续使用 | `TestLinuxPTYInteractiveMVP` | 通过；无需额外换行，无自动总结请求 |
| 显式 shell 与同组子进程运行中 Ctrl+C | `TestLinuxPTYInteractiveMVP`、`TestLinuxCancellationKillsProcessGroup` | 通过；本地收尾不超过 2 秒，普通同组子进程终止 |
| HTTP 503 有界重试失败后继续追问 | `TestLinuxPTYInteractiveMVP` | 通过；单次调用共 3 次 HTTP 尝试，密钥未显示 |
| 单个 Turn 连续九次工具往返，第十次文本回答 | `TestLinuxPTYLongTurnClarificationRetryAndContextLimit` | 通过；没有旧版八步中断 |
| 普通澄清问题与回答、退避等待 Ctrl+C | `TestLinuxPTYLongTurnClarificationRetryAndContextLimit` | 通过；退避取消后未发第二次 HTTP 请求 |
| 上下文超限提示、显式 `/clear` 后新请求 | `TestLinuxPTYLongTurnClarificationRetryAndContextLimit` | 通过；清空后请求只含新用户输入 |
| 正整数 `--max-steps 1` 的最后调用跳过与下一轮追问 | `TestLinuxPTYOptionalStepLimit` | 通过；未请求命令授权 |
| 就绪 EOF、授权 EOF、`/quit`、隐藏密钥与空密钥 | `TestLinuxPTYEOFAndHiddenCredential` | 通过；空密钥拒绝，隐藏输入不回显 |
| EOF 发生于命令运行且收尾失败 | `TestLinuxPTYEOFDuringCleanupFailureExitsNonzero` | 通过；显示原始取消原因与残留风险，进程非零退出 |
| 首条命令清理失败，同批剩余调用跳过，记录可查且后续追问不重跑 | `TestLinuxPTYCleanupFailurePairsBatchAndStopsModel` | 通过；失败当轮没有续接模型请求 |
| `/status`、`/tools`、`/clear`、`/exit` | 上述 PTY 测试 | 通过；发行版只有 `run_command`，默认上限显示不限 |

Linux 真实命令测试还覆盖主进程正常或非零退出后同组后代仍运行、后代持有输出管道、重复取消、连续调用不累积文件描述符、不影响其他进程组和启动失败不误杀；见 `TestLinuxNormalExitCleansDescendantsAndPipes`、`TestLinuxOwnershipDoesNotKillOtherGroupOrAccumulateFDs`、`TestLinuxRepeatedCancellationHasOneResult`。主动脱离进程组但持有管道的后代触发 1 秒管道等待上限与 `cleanup_failed`，见 `TestLinuxDetachedPipeHolderHasBoundedFailure`；测试按 PID 清理该后代。最小 WSL init 可能暂时留下 zombie，测试以进程不可继续执行（`ESRCH` 或 `Z` 状态）为终止依据。

## 协议、Agent 与清理故障验证

- Responses 请求使用本地 HTTP/SSE，断言 `/responses` 路径、Bearer、`stream: true`、`store: false`、`parallel_tool_calls: false`、完整历史和无压缩/截断参数。reasoning 加密字段、message 原始字段、多个 function call 与 `call_id` 按原序只续传一次。
- SSE fixtures 覆盖 LF/CRLF、多行 data、注释、未知事件、非法 JSON、缺少完成事件、完成后仍保持连接以及单事件 8 MiB 上限。收到任何 data 后断流不重发，也不返回可执行的部分工具。
- 重试 fixtures 覆盖 HTTP 408/429/500/502/503/504、瞬时读取失败、第三次失败终止、无效证书/认证/普通 400 直接失败、Retry-After 秒数与日期、超过 30 秒放弃、同一输入快照和共享调用超时。工具完成后的模型续接失败只重发模型请求，工具执行次数保持一次。
- 上下文超限只根据明确 `context_length_exceeded` 分类；HTTP 400、输出长度限制、错误体中的测试密钥不误判或回显。工具之后超限保留历史与记录，用户显式 `/clear` 后才从新输入开始。
- 可注入的私有平台操作分别模拟终止请求失败、直接进程未回收、清理等待耗尽。所有结果为 `failed/cleanup_failed`，保留 `termination_reason`、已取得输出和适用退出码；另有 Linux 真进程测试，不以故障桩替代。
- CLI 输入测试使用可控终端与管道，核对旧授权不转移、EOF/取消不需要额外换行、只有一个输入消费者、后续管道行保留。管道 EOF 在模型运行中会取消当前请求。

## 公开工具包跨模块验收

在独立临时 Go 模块中添加 `require host-scout v0.0.0` 与本项目路径的 `replace`，只导入 `host-scout/pkg/tool`；fake Tool 实现 `Definition()` 和 `Run(context.Context, json.RawMessage) (tool.Result, error)`，通过 `tool.NewRegistry` 注册、发现定义、执行并运行 `go test ./...`，结果通过。临时模块随后删除。仓库内的 `TestRegistryRejectsInvalidDefinitions`、`TestRegistryDispatchAndSnapshots` 与 `ExampleNewRegistry` 可重复验证公开入口；原 Agent 的 fake Tool 测试验证取消、事件和工具往返。

## 复现

有 Go 的 Linux 环境可在项目根目录运行：

```bash
go test ./... -count=1
go test ./internal/cli -run TestLinuxPTY -v -count=1 -timeout 90s
go test ./internal/tools/command -run 'TestLinux|TestCleanup' -v -count=1 -timeout 30s
go vet ./...
```

本次 Windows 主机上的 Linux 实测方式：分别以 `GOOS=linux GOARCH=amd64 go test -c` 编译 `internal/cli`、`internal/tools/command`、`internal/agent`、`internal/providers/openairesponse` 的测试二进制，再用 `wsl -e sh -lc './<测试二进制> -test.timeout=90s'` 运行。四组均返回 `PASS`，临时二进制已删除。Windows/macOS 的 `go build ./...` 只证明编译通过。

## 第三方端点在线冒烟

使用 `https://api.oken.ai/v1`，只在临时进程环境中传入用户提供的密钥；未写入项目文件、命令行参数或本记录。`GET /v1/models` 返回 HTTP 200，列出 `gpt-5.6-sol` 等模型。`POST /v1/responses` 使用 `gpt-5.6-sol` 返回 HTTP 200、`text/event-stream` 和 `response.completed`。项目 CLI 以该模型完成一轮“只回答 OK”的请求，显示 `assistant> OK`、`Turn completed.`，退出码为 0。

最初的工具轮次未能完成在线验收：`gpt-5.6-sol` 的 CLI 请求工具时，服务端连续返回 HTTP 503；直接发送最小 function tool 请求时也返回 HTTP 503，错误码为 `system_cpu_overloaded`。改用 `gpt-5.6-luna` 再试时返回 HTTP 403，错误码为 `insufficient_user_quota`，因此当时停止请求。更早尝试 `gpt-6-luna` 的普通 CLI 对话也遇到 HTTP 503。

随后按用户选择改用 `gpt-6.1-sol`。项目 CLI 的纯文本轮次回答 `OK` 并正常结束。第二轮在线验证中，模型提议 `run_command` 执行 `argv ["go","version"]`；由于测试输入是非终端管道，CLI 按规则自动拒绝执行，生成 `evidence-001`。模型收到 `denied` 结果后明确回答命令未执行、无法报告版本；`/evidence` 可查到该记录，进程正常退出。这验证了该模型的 function call、拒绝结果续传和记录查询；**获批后的真实命令执行仍未在线测试**。服务端先前的错误和额度状态不构成本地代码通过或失败的结论。

## 未验证项与范围

- 未连接真实 OpenAI API；第三方端点已验证工具拒绝结果续传，尚未在线验证获批后的真实命令输出续传。自定义端点的完整协议边界仍以本地模拟服务测试为主要证据。
- 未执行 race detector，也未在真实 macOS 或 Windows 终端中验收 Ctrl+C/进程清理。Windows/macOS 只有直接进程清理保证，不声明 Linux 同组后代保证。
- 不验收主动脱离进程组后的终止、已发生副作用的回滚、后台命令会话、PTY 工具、其他远程 API、自动压缩、持久化或完整脱敏；这些不属于 v1 交付范围。

## OpenSpec 校验记录

执行 `openspec validate v1-interactive-agent-mvp --strict`，结果：`Change 'v1-interactive-agent-mvp' is valid`。此项仅确认规格结构有效；代码验收依据是上述单元测试、Linux 实测、终端验收与构建结果。
