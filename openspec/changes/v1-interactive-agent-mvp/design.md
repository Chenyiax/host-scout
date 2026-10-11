# Design

## Context

动机和交付范围见 [proposal.md](proposal.md)。2026-10-11 修订仍属于同一个未归档的 v1 变更；旧版 27 项任务已完成，但下面的新增设计不表示已经实现。受影响任务重新打开，未受影响的历史完整性、授权和 SSE 能力继续复用。

当前代码已经提供以下骨架：

| 模块 | 已有行为 | 本期需要补齐 |
|---|---|---|
| `internal/model` | 通用 Client、消息/工具调用/不透明载荷和错误类别 | 上下文超限错误；工具定义与公开契约衔接 |
| `internal/agent` | 顺序循环、忙碌保护、历史检查点、事件和取消 | 默认不限的循环、可选上限和重试后不重放 |
| `internal/providers/openairesponse` | HTTP/SSE、完整输出续传、8 MiB 事件限制 | 小型有界重试和上下文超限识别 |
| `internal/cli` / `config` | 逐轮输入、命令审批、Ctrl+C、凭据与参数 | 显式 TOML 配置、优先级、不限状态和退避取消 |
| `internal/tools` / `policy` | Registry、两个 `/proc` 工具、统一结果、取消/超时进程组清理 | 单命令发行版、公开工具契约、一次性执行所有权与清理失败传播 |

现有 fake Client、工具契约、Responses 模拟服务与 Linux 实际执行测试验证了增量提交、调用配对、拒绝和取消、密钥环境隔离与输出截断。复用这些机制，不重新建立 Agent 框架。当前缺口是默认 8 步且强制 1..100、工具契约仅在 internal、Linux 注册三个工具、没有配置文件和请求重试、上下文错误没有专门类别。命令目前在取消/超时杀进程组，但不主动收尾正常退出后残留的同组后代，取消状态还可能覆盖终止操作的错误；新版生命周期和失败契约需另行实现与验证。

README 包含执行中消息插入、权限分级、脱敏、持久化和报告等长期目标。本期以 proposal 与四份 specs 的范围为准，在实现完成时将 README 的“第一期”与长期目标分开描述。

## Goals / Non-Goals

**Goals:**

- 沿用现有 Go 模块和小型会话循环，将用户可见行为与模型协议分层。
- 建立可测试的轮次终止、调用关联和历史提交契约，支持取消后继续使用。
- 参考本地 Codex 源码的“需要工具续接就继续，完成或中断就退出”语义，内核只负责推进 Turn、历史和工具调度；不复制其 hooks、steering 或多 Agent 机制。
- 以本地模拟服务和 fake 组件完成主要验收，真实 API 仅用于可选人工冒烟。

**Non-Goals:**

- 不设计通用 Agent SDK、插件平台、复杂 planner 或工作流 DSL。
- 不为多个 API 建立完整能力矩阵；只预留窄接口，不实现第二种远程协议。
- 不在本期拆出完整 executor、policy engine、evidence store 等框架。现有模块可承载最小实现，职责清晰后再按需要拆分。
- 不将用户的自然语言约束转换成确定性的安全规则；上下文影响模型建议，实际命令仍逐次审批。
- 不做上下文压缩、摘要、自动裁剪、本地 token 预测或费用预算；模型与工具各保留最小扩展接口即可。
- 不引入后台命令管理器、PTY、stdin 续写、执行会话 ID 或进程池；一次性调用的内部资源所有权不作为公开 Agent/Executor SDK。

## Decisions

### 1. 逐轮交互，单会话单执行者

一条普通输入启动一个 Turn；使用简单循环推进模型调用和顺序工具执行。有工具调用就按校验、授权、执行、提交结果的顺序处理，然后继续模型；同一响应含文本和工具时，文本不是停止信号。完整响应含可见回答或澄清问题且没有调用时结束 Turn。澄清通过普通助手文本提出，用户回答进入新 Turn；不做独立 question tool、方向引导、planner、Stop hooks、输入队列或后台任务。

循环每次模型调用前检查取消和可选上限；默认 `max_steps=0` 不按次数停止，正整数才启用护栏。最后允许的响应若仍提出工具，不执行该批、补齐 step_limit 结果后结束；避免产生无法继续反馈的新增主机副作用。最终文本仍可成功。内核不按请求次数硬编码诊断阶段。

`Session` 对并发 Ask 使用忙碌保护并立即返回 busy，不让第二轮等待锁后隐式开始。读记录返回快照；Clear 只在就绪态执行。运行期只有一个执行者修改历史和调用记录，事件回调不允许重新进入 Ask 或 Clear。

考虑过执行中持续注入普通消息，但这会同时涉及输入归属、在途请求更新和待执行动作重评估。当前以“取消—回到提示符—补充后继续”的明确路径满足基本交互，后续可在事件契约上扩展。

### 2. 保留窄模型接口，补齐通用历史

保留当前 `model.Client.Create(ctx, request, onText)` 这一调用形状。模型选择、端点、密钥、HTTP 和 SSE 均在具体 Client 中；运行时仅消费通用 Request/Response。指令通过会话 options 注入，CLI 组装 Host Scout 的默认指令，内核不硬编码产品 Prompt。

保留 `Item.Kind` 的用户、助手、工具调用、工具结果与 opaque 有限集合，以及工具名称、参数和 Provider 归属。通用语义示意如下，具体 Go 类型保持简洁：

```text
Request  = Instructions + ordered Items + ToolDefinitions
Response = ordered Items + ToolCalls + visible Text
Item     = Kind + Text + CallID + Name + Arguments + optional ProviderPayload
Payload  = ProviderID + opaque raw data
```

每个 Provider 输出在 `Items` 中仅有一份：助手消息和工具调用有通用字段，必要时同时携带原始载荷；reasoning 等续传项以 opaque item 保存。`Calls` 是同一批 Items 的执行视图，不能产生第二份历史。适配器保证二者一致，运行时校验调用标识和关联。

Responses 编码时，匹配其 ProviderID 的完整载荷优先原样使用；没有载荷的通用 item 则由适配器编码。其他 Provider 不得解析 Responses 载荷；本期不支持在同一会话跨 Provider 切换，启动时固定模型实现。

考虑过仅保留全量原始 Responses JSON，无法让无 Responses 数据的 Client 工作；也考虑过把全部远程 item 规范化，容易丢失续传字段。采用通用语义加可选不透明载荷，在保留协议保真度的同时提供可替换边界。不新增 `--provider` 选项、自动探测或第二适配器，接入扩展通过启动组装完成。

### 3. Responses 仅采用完整响应驱动工具执行

继续原生 HTTP/SSE 实现。请求固定 `stream: true`、`store: false`、`parallel_tool_calls: false` 和本地 function tools。`base-url` 是包含 API 前缀的基址，由适配器追加 `/responses`；不使用 `previous_response_id`，不实现远程会话恢复、后台模式或服务端取消接口。

SSE 解析器负责事件边界、CRLF、多行 data、注释、非法 JSON 及单事件合计 8 MiB 限制。每行和累计事件均须有界。收到有效 `response.completed` 后即可结束本地读取，不要求额外 `[DONE]` 或等待连接 EOF。只有完整响应的最终 function arguments 可供执行，参数增量只被消费或忽略。

完整 output 按原序保留，尤其是 message 的原始字段、function call 和 reasoning 的加密载荷。官方 [function calling 文档](https://developers.openai.com/api/docs/guides/function-calling)说明了工具调用与结果通过 `call_id` 对应的往返方式；[流式文档](https://developers.openai.com/api/docs/guides/streaming-responses)定义了文本增量与完成事件。[reasoning 文档](https://developers.openai.com/api/docs/guides/reasoning)要求手动维护状态时保留续传项。适配器不解释加密内容，不将其展示为模型思考过程。

考虑过根据参数 done 事件提前执行，会使部分响应失败与主机操作纠缠；MVP 以完整模型响应为执行边界。

重试完全封装在一次 Client.Create 内：尚未收到任何 SSE data 的瞬时连接/读取失败，或 HTTP 408/429/500/502/503/504，最多额外尝试两次；无效证书等永久网络错误、认证、协议、上下文超限、remote_failed/incomplete 和取消直接返回。收到 response.created 等任意 data 后即禁止自动重发；注释/心跳不算 data。第一次和第二次退避基准分别为 500 ms、1 s，以同一输入快照重建请求；合法 Retry-After（秒数或日期）不得被提前截短，超过 30 秒则放弃重试。所有退避和 HTTP 读取受同一调用 context 控制，整个 Create 默认最多 5 分钟，不因重试重置总超时。重试计数每次 Create 独立，不重开 Turn、不提交失败尝试、不发出重复工具事件。

错误体只做有界解析以识别稳定错误码，不回显原文；明确的 context_length_exceeded 映射为通用 context_limit，不把普通 HTTP 400 或 max_output_tokens 不完整响应误判为上下文超限。未知服务错误保留原来的安全通用分类，不猜测上下文容量。

### 4. 轮次检查点和调用终结

历史提交分为三个时点：

1. 接受非空用户输入后立即记录该输入。
2. 模型完整响应在全批调用关联校验通过后提交；部分响应或关联非法的响应不提交。
3. 每个工具终结后立即记录其结果。在轮次返回前，所有已经提交的待处理调用都补齐一个终结结果。

终结原因包括用户拒绝、校验错误、执行失败、取消以及 `step_limit`。本轮被取消或达到限制时，不使用额外模型请求“补写总结”；仅为尚未终结的调用生成明确的未执行结果，使下一次用户输入可在合法历史上续接。

示例：

```text
用户问题 → 完整模型响应 [call-A, call-B]
call-A 成功 → 立即记录 result-A
call-B 等待确认时取消 → 记录 result-B(cancelled, not executed)
本轮取消 → 就绪
下一轮用户追问 → 续传完整历史，不重新执行 A 或 B
```

`max_steps` 只计算本 Turn 调用 Client.Create 的次数，HTTP 尝试数由 Provider 独立限制，每步最多三次；它不是物理请求/费用预算。0 表示不限，正整数表示上限，负数和溢出拒绝。历史中曾用过的调用 ID 不可再次作为新执行请求，避免重复调用和结果覆盖。后续模型请求的网络重试只重发同一历史，不能重跑之前的工具或重做授权。

模型请求在工具完成后失败，仍保留工具结果。错误或取消前已显示的 partial text 仅作为用户看过的流式内容，不纳入成功助手历史，CLI 明确标记本轮未完成。

考虑过失败时回滚整轮，会丢失已经发生的操作；保留未配对调用会破坏续传。采用增量提交和补齐终结结果，只保证历史与已发生事实一致，不自动重放任何操作。

### 5. 轻量事件与明确错误类型

沿用统一 `OnEvent` 和兼容文本/工具回调。Provider 的 onText 由内核转为事件；不需要 channel 事件总线、订阅系统或专门的重试状态机。回调按序、同步处理，不自行触发执行。

| 事件 | 主要信息 |
|---|---|
| `turn_started` | 轮次开始 |
| `text_delta` | 文本 |
| `tool_proposed` | CallID、工具名 |
| `approval_requested` | CallID、精确 argv |
| `approval_resolved` | CallID、allowed / denied |
| `tool_started` | CallID、工具名，表示已获准且开始执行 |
| `tool_finished` | CallID、记录 ID、结果状态 |
| `turn_finished` | completed / cancelled / failed / step_limit |

非命令工具没有审批事件。审批遇到取消或 EOF 时无需发出虚构的审批决策，仍须发出工具终结和轮次终结。构造或输入错误、busy 在接受轮次前返回，不生成 turn_started。

模型错误区分 transport、http、protocol、incomplete、remote_failed、cancelled 和 context_limit。重试耗尽保留最后失败类别，不新增庞大错误体系。工具结果区分 succeeded、failed、denied、cancelled、timed_out、skipped。命令清理失败使用已有 failed 状态和 cleanup_failed 错误码，另保留 termination_reason（completed、nonzero_exit、cancelled 或 timeout），不被 ctx.Err 覆盖；Agent 将此错误作为当前 Turn 的终止条件，剩余调用补齐 skipped/cleanup_failed，不再请求模型。输入组件报告 EOF 时取消当前工作并关闭 CLI，不继续模型循环；关闭收尾失败则明确显示风险并以非零状态退出。

考虑过继续只用文本日志，无法可靠展示“待确认”和“已执行”，也不利于测试取消；采用小型事件枚举，暂不做完整状态机框架。

### 6. 单一内置工具与公开最小契约

将工具契约放入公开 `pkg/tool` 包，包含独立的 Definition、Result、Status、Tool 和 Registry；不得通过别名继续暴露 internal/model 的类型。最小形状为 Definition() Definition 与 Run(ctx, json.RawMessage) (Result, error)，Registry 在启动时接收实例并生成定义/分发；非法或重复定义直接失败。内部模型可引用公开定义，公开工具包不导入 internal 或具体 Provider。用独立临时 Go 模块验证可导入性，用原 Agent 的 fake 工具验证注入闭环；不额外公开完整会话 SDK。

所有平台的标准 CLI 只组装 run_command；移除 /proc 工具的默认注册和“只读工具不足时才用命令”的指令，旧 /proc 实现不属于 v1 交付或验收要求。系统与进程观察通过模型提出命令完成，仍逐次授权。测试/后续组装可以注入额外工具，但不改变发行版的唯一内置集合。不做运行中注册、动态加载、工具下载、MCP 或风险分级。

command 继续使用 policy.Approver 发出授权事件。执行 Hook 只在参数有效且命令获准后、访问主机前上报开始；扩展工具也须尊重校验、取消和真实开始通知契约。这样不会将等待确认或被拒绝的调用展示为已执行，无需完整 Action/Executor 框架。

保留 `/evidence` 名称作为轻量调用记录查询。每条记录包含 ID、CallID、工具、起止时间、status、code 和有界输出，command 额外包含实际 argv、退出码和截断标识。失败、拒绝和 skipped 同样可查询，但明确区别于成功观察。对应的工具结果回传模型时携带记录 ID。

统一结果延续已有状态、数据、argv、可空退出码、输出和截断标记。结果必须有界且可序列化，错误不能伪装为成功；运行时仍负责关联 CallID 和记录，不要求工具解析 Responses。v1 不抽取 Finding、不自动验证引用、不生成结构化报告。

考虑过立即增加完整 executor 和证据库，超出本期闭环需要。小型结果与事件足以预留后续拆分点。

### 7. 可取消的终端输入归属

将直接 Scanner 调用集中到一个 CLI 输入组件，供普通提示符和审批共用，任一时刻只有一个输入消费者。审批等待通过输入事件与 cancel/EOF 选择，不在批准回调内不可取消地阻塞。就绪态也注册 Ctrl+C 处理，清除未提交输入后重新显示提示符。

取消后撤销当前授权提示，已排队的授权回复不得转移到下一次审批；不得为每次 Ask/审批创建一个永久阻塞的读输入 goroutine。终端取消需要清理未提交输入，管道模式按行顺序读取，不预读后丢弃脚本的下一行。取消路径必须在无额外换行的条件下通过终端验收。

普通输入只在就绪态提交，授权态仅解析当前确认；忙碌时不承诺处理普通消息，不将键盘缓冲当作受支持的消息队列。`/cancel` 暂不实现，取消入口为 Ctrl+C；其他本地命令仅在就绪态处理。需要支持控制字符的最低限度终端处理应封装在输入组件，不扩展为完整 TUI。

考虑过对 Scanner 包一层 context 却仍等待 Scan 返回，这不能满足审批取消。采用可取消输入组件，将平台相关读取留在 CLI，Agent 只接收 context 和审批结果。

### 8. 主机执行预算与平台边界

保留 command 的 10 秒执行预算、32 KiB 合并输出、argv 32 项/4 KiB、无自动 shell 和环境白名单。退出码使用可空值，非零退出、超时和取消均为明确状态；先记录外层取消或工具自己的 deadline 等原始原因，再单独判定收尾是否成功，cleanup_failed 优先于普通取消/超时结果。主进程真实退出码（包括 0）不代表整次调用清理成功。执行预算与收尾等待分开，通常上限为 10 秒执行加最多 2 秒收尾，不宣称命令必须在 10 秒内返回结果。

每次已批准调用采用私有的小型执行所有者，绑定直接进程、Linux 进程组身份、输出管道/读取任务与收尾状态；Agent 只等待工具结果，不自行管理 PID 或系统信号。必须区分“直接进程退出”和“输出 EOF”，不让 stdout/stderr 的复制等待掩盖主进程退出。主进程正常或非零退出后，同组后台后代也属于本次资源并收尾，不支持命令返回后留作受托管后台服务。启动失败未取得有效进程身份时不向 0 或其他不可信进程组发信号；只清理本次拥有的进程，避免重新查询已结束 PID 的进程组归属。

Linux 创建独立进程组，显式 shell 及普通后代同样受管理；收尾以 SIGKILL 终止本次进程组，不添加 SIGTERM 等待或可配置信号策略。收尾入口幂等：取消、超时、正常退出与重复取消共用一个终结路径，不创建多个杀进程/读输出任务。收尾使用独立且有界的清理期限，不复用已经取消的执行 context 导致跳过清理，也不通过脱离调用的后台执行维持命令运行。

取消、超时或主进程退出触发收尾后，本地等待总计不超过 2 秒；管道排空等待最多 1 秒并包含在这 2 秒中，不能叠加成额外无界等待。回收直接进程、关闭/停止 I/O 并检查平台终止错误后才返回结果；普通后代由系统回收，不要求 Agent 充当 subreaper。发信号不等于回收成功：终止失败、未能回收直接进程或收尾超限均返回 failed/cleanup_failed，保留已取得的输出与 termination_reason。Linux 真实进程测试将不存在或僵尸状态视为已停止执行，不承诺操作系统异常下强制回收一定成功。

工具返回明确收尾结果后补齐历史，再发 TurnFinished 和解除 busy；失败场景不无限等待，也不伪报成功后继续本轮。CLI 单独显示可能残留进程的风险，下一轮不重放旧调用。Linux 与非 Linux 实现保持 build tags；Windows/macOS 开发模式仅保证直接进程及本地 I/O 收尾，不在一期加入 Job Object 或承诺完整进程树清理。主动脱离进程组的进程与已有副作用不在回滚范围，也不新增对宿主崩溃或强制退出的保证。

考虑过复制 Codex 的 PTY、后台执行会话、进程池和平台 Job Object，这些解决的是跨调用交互和长运行命令。采用一次性所有权与进程组足以满足本期取消体验；正常返回也收尾同组后代，使后台服务不成为隐式能力。不新增容器、沙箱或完整执行框架。

### 9. 显式配置文件与优先级

新增 --config <path>，仅显式读取一个 TOML 文件，相对路径以当前工作目录解析，不自动搜索、创建或写回。支持顶层 model、base_url、max_steps、allow_insecure_http；缺省字段沿用环境/默认值，未知字段、无效类型、语法、缺失显式文件、负数或整数溢出在网络访问前失败，不打印字段值。密钥仍只由环境或 TTY 隐藏输入获得，不接受配置 api_key。

模型/端点优先级为显式 flag > 对应环境变量 > 文件 > 默认；上限与不安全 HTTP 开关为显式 flag > 文件 > 默认。必须跟踪 flag 是否显式提供，避免 --max-steps 0 或 --allow-insecure-http=false 被文件覆盖。默认上限 0，不再强制 100 的人为上界，启动与 /status 展示“不限”或实际正整数。文件即使被 flag 覆盖也先严格校验，避免用户误以为无效配置已生效。配置示例：

```toml
model = "your-model"
base_url = "https://api.openai.com/v1"
max_steps = 0 # 默认不限，可显式改为正整数
allow_insecure_http = false
```

选择成熟小型 TOML 解析器而非手写解析；只承载少量启动字段，不扩展 profiles、热加载或配置系统。

### 10. 一期不处理上下文压缩

每次模型调用仍传递完整有效历史，保留 reasoning 等不透明项；不设置服务端自动压缩、自动截断，也不调用 /responses/compact 或额外摘要模型。明确的上下文超限直接终止 Turn，保留所有已完成调用并提示用户显式 /clear。这是有限模型窗口下的明确停止条件，不将“无次数上限”解释为无限上下文。

## Risks / Trade-offs

- [命令以当前用户权限执行，argv 可显式启动 shell] → 展示精确 argv、逐次审批、限时限量；不将“无自动 shell”描述为沙箱或只读保证。
- [尚无完整脱敏，工具输出可能包含业务敏感信息] → 保留预览定位，启动和授权时明确输出发送行为；仅保证不主动注入模型凭据，并屏蔽错误中的已配置密钥，不承诺对任意文件或输出的完整秘密检测。
- [取消不能保证远端计算停止或撤销主机副作用] → 区分本地停止与操作回滚，保留已完成记录；不报告未经确认的恢复动作。
- [主进程退出、信号发送成功不等于所有资源已收尾] → 命令统一收尾后才返回；cleanup_failed 保留原始原因并停止本轮，输出与真实退出码不作为清理成功依据。
- [正常退出时收尾会终止有意留下的同组后台服务] → 一期明确仅支持一次性命令，不提供后台服务契约；脱离进程组不被描述为受管理或可靠保活方式。
- [终端输入取消有平台差异] → 输入与命令执行采用平台边界；Linux 真终端验收为发布要求，开发平台运行与编译单独验证。
- [历史在内存中累积，可能达到模型上下文上限] → v1 不自动摘要或丢弃历史；服务拒绝时明确报错并允许 `/clear`，上下文预算后续增加。
- [默认持续运行可能增加调用数和费用] → 启动明确展示“不限”，支持显式 max_steps 和随时取消；每步重试有独立上界，不将 max_steps 宣称为费用预算。
- [重试可能让服务端重复计算当前模型请求] → 只在流 data 之前重试，失败尝试不提交、不执行工具；不承诺远端请求 exactly-once。
- [compatible 服务并非完整支持 Responses] → 定义最低 HTTP/SSE/function-call 契约，以本地协议 fixtures 覆盖；不自动降级、不声称支持所有代理服务。
- [模型未必理解用户约束或引用正确证据] → 内核验收验证上下文传递、授权和真实执行状态；模型理解质量通过人工对话检查，不把自然语言测试变成确定性安全保证。

## Migration Plan

1. 建立公开最小工具契约，迁移内部依赖，标准 CLI 仅注册 command；复用现有结果/审批测试并补外部模块验收。
2. 添加配置文件与不限值，再改简单循环和上限判断；复用历史检查点与事件，不引入新状态机。
3. 在 Provider 当前请求内补有界重试和 context_limit，验证部分流不重试、同一工具结果不会重复执行。
4. 补一次性命令所有权、正常退出收尾、有界 I/O 和 cleanup_failed 的工具/Agent/CLI 传播，复用基础进程组测试并增加失败注入与后代隔离测试。
5. 验证 Ctrl+C/EOF 贯穿 shell 和退避，重新完成 Linux 真终端、构建及四份规格覆盖。随后更新 README 和验收记录，不提前将规划标记为已交付。
6. 无存储格式迁移。恢复旧版本时重新启动会话即可，不尝试在不同内部类型间迁移在途历史。

验收以四份 specs 中的场景为准。交付检查包含本地契约测试、Linux 命令进程组测试、Linux 真终端 Ctrl+C/审批/EOF 检查及无 CGO 的 Linux 构建。没有 Linux 执行环境时只能完成跨平台测试和交叉编译，不能据此宣布 Linux 取消验收通过。
