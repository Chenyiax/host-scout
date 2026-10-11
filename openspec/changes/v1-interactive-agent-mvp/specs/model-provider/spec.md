# Spec Delta

## Purpose

定义 Agent 调用模型所需的最小通用契约，以及 v1 唯一交付的 Responses API 适配行为。将文本、工具调用和取消等会话语义与远程协议分离，使后续 API 可通过新增适配器接入，并保持已有工具和交互契约。

## ADDED Requirements

### Requirement: Provider 独立模型契约

模型接口 SHALL 接受指令、按序历史、工具定义和取消信号，流式提供文本，并返回完整响应或明确错误。通用历史 SHALL 表达用户消息、助手消息、工具调用与工具结果；协议特有续传载荷可以不透明保存。新增 API 适配器不得要求 Agent、工具或终端解析其远程字段。

#### Scenario: 无 Responses 数据的替代实现

- **WHEN** 一个测试 Provider 仅返回通用文本和工具调用，不包含 Responses JSON
- **THEN** 相同运行时可以完成两轮聊天以及一次工具往返，而无需修改工具或终端逻辑

#### Scenario: Provider 续传载荷隔离

- **WHEN** 历史包含当前适配器返回的不透明载荷
- **THEN** 运行时仅保存和传递该载荷，不读取 reasoning 等远程字段；会话清空时一并移除

### Requirement: Responses 是 v1 唯一远程协议

v1 SHALL 仅通过 HTTP POST 调用配置端点下的 `/responses`，使用 Bearer 认证、`stream: true`、`store: false` 和 function tools，并请求关闭并行工具调用。自定义端点 SHALL 满足相同协议；不得自动调用其他 API、Conversations 或 `/responses/compact`，不得启用服务端自动压缩。失败请求仅按本规格的有界重试契约重试。

#### Scenario: 默认请求行为

- **WHEN** 用户向默认端点提交一轮问题
- **THEN** 请求发送到 `/v1/responses`，携带指定模型、指令、历史和工具定义，且声明流式、无存储和不并行调用

#### Scenario: 自定义端点缺少协议支持

- **WHEN** 配置端点返回普通 JSON 而非要求的 SSE
- **THEN** 本轮报告协议不兼容，不尝试其他 API 或执行其中未确认的工具调用

### Requirement: 当前请求的有界重试

适配器 SHALL 只对尚未收到任何 SSE data 事件的瞬时连接/读取失败，以及 HTTP 408、429、500、502、503、504 重试，每次模型调用最多额外两次 HTTP 尝试。退避 SHALL 有界且可取消；合法 Retry-After SHALL 被尊重，要求等待超过 30 秒时停止重试。重试 SHALL 复用同一输入快照，不提交失败尝试、不重启 Turn、不执行工具。取消、配置/认证错误和永久网络错误不得重试。

#### Scenario: 瞬时失败后成功

- **WHEN** 当前请求在任何 SSE data 前连接失败，第二次尝试完整成功
- **THEN** 调用方得到一次完整响应，失败尝试没有历史项或工具事件，两次尝试使用相同输入和工具定义

#### Scenario: 重试预算耗尽

- **WHEN** 同一模型调用连续三次遇到可重试失败
- **THEN** 第三次后返回明确错误，不发起第四次尝试，下一次用户输入不自动重放已完成操作

#### Scenario: 收到流数据后断开

- **WHEN** 请求已经收到 response.created、文本、工具参数或其他 SSE data，随后连接中断
- **THEN** 返回未完成或传输错误，不自动重发，不执行部分工具调用，避免重复文本和不可辨认的恢复边界

#### Scenario: 重试等待期间取消

- **WHEN** 适配器正在退避时调用方取消
- **THEN** 立即解除等待并返回取消，不发起下一次 HTTP 尝试

#### Scenario: 认证或永久连接错误

- **WHEN** 端点返回 HTTP 401/403，或连接因无效证书等永久错误失败
- **THEN** 当前调用直接失败，不消耗多次 HTTP 尝试掩盖配置问题

#### Scenario: 服务要求较长等待

- **WHEN** 可重试 HTTP 响应的合法 Retry-After 要求等待 60 秒
- **THEN** 本次调用停止并报告失败，不提前重试，也不无限延长本地等待

#### Scenario: 工具之后重试模型

- **WHEN** 工具已经成功并提交结果，续接模型请求在 SSE data 前遇到瞬时失败
- **THEN** 仅重试携带既有调用与结果的模型请求，不重新执行工具或重新请求命令授权

### Requirement: 流式文本与完成判定

适配器 SHALL 按 SSE 事件提供文本与拒绝文本增量，仅在收到有效 `response.completed` 且最终状态为 `completed` 时返回完整响应。function call 的参数增量不触发主机执行；执行使用完整响应中的最终参数。

#### Scenario: 参数尚未完成

- **WHEN** 流中到达 function call 的部分参数但没有完整完成响应
- **THEN** Agent 不执行该调用，即使片段暂时能解析为 JSON

#### Scenario: 流式完成

- **WHEN** 流包含多个文本增量及有效的完成事件
- **THEN** 增量按序交付，最终返回完整文本和可续传输出，不重复触发文本显示

#### Scenario: 完成后连接仍未关闭

- **WHEN** 服务发出有效的完成事件但没有立即关闭连接或发出 `[DONE]`
- **THEN** 适配器返回完整响应，不为等待连接 EOF 阻塞当前轮次

### Requirement: SSE 解析和未知事件

适配器 SHALL 正确处理 LF 或 CRLF、同一 SSE 事件的多行 data、注释及非 data 字段。未知非终结事件 SHALL 可忽略，`[DONE]` 不替代完整完成事件。单个 SSE data 载荷 SHALL 有 8 MiB 上限；非法 JSON、超限或未完成事件结束 SHALL 返回协议错误。

#### Scenario: 心跳和未知事件

- **WHEN** 正常完成的流中混入注释、心跳或未知非终结事件
- **THEN** 适配器仍正确交付文本和完整响应

#### Scenario: DONE 之前缺少完成事件

- **WHEN** 流只输出部分文本和 `[DONE]` 就关闭
- **THEN** 返回未完成协议错误，不把部分文本当作成功回答

#### Scenario: data 载荷超限

- **WHEN** 同一事件的一行或多行 data 合计超过 8 MiB
- **THEN** 停止解析并返回明确错误，不无限增长缓冲区

### Requirement: 完整输出与工具结果续传

适配器 SHALL 按原顺序无损保留完整输出中的 message、function call、reasoning 及其不透明续传字段，并在后续请求中结合对应 `function_call_output` 续传。每个输出只续传一次，工具结果使用原 `call_id`；v1 不依赖 `previous_response_id` 或远程持久会话。

#### Scenario: reasoning 与多个工具调用

- **WHEN** 完整输出依次包含 reasoning、两个 function call 和一条 message
- **THEN** 下一次请求保留四个输出的相对顺序与原始续传字段，并含两个关联正确的工具结果

#### Scenario: 返回加密续传字段

- **WHEN** reasoning item 带有 `encrypted_content`
- **THEN** 该字段原样续传，不当作助手可见文本展示或工具输出记录

### Requirement: 模型失败与取消

适配器 SHALL 区分网络错误、非 2xx HTTP、流中 error、`response.failed`、`response.incomplete`、协议错误和调用方取消。失败不得返回可执行的部分响应；取消 SHALL 中止本地 HTTP/SSE 工作，不要求服务端取消接口，也不承诺远端计算停止。

#### Scenario: incomplete 响应

- **WHEN** 服务返回 `response.incomplete`，原因为输出限制
- **THEN** 返回不完整错误及可安全展示的原因，不执行部分工具调用、不自动补发请求

#### Scenario: 调用方取消连接

- **WHEN** 调用方在等待流事件时取消请求
- **THEN** 本地请求与读取结束，返回取消状态，而非把关闭连接报告为普通网络故障

### Requirement: 上下文超限错误归类

适配器 SHALL 将服务明确返回的 `context_length_exceeded` 错误码归类为 Provider 独立的上下文超限错误，不将所有 HTTP 400 或 max_output_tokens 不完整响应误判为上下文超限。错误 SHALL 使用安全可展示信息；上下文超限不自动重试、不压缩、不裁剪输入，不返回可执行的部分响应。

#### Scenario: HTTP 上下文超限

- **WHEN** 非成功 HTTP 响应包含可验证的 context_length_exceeded 错误码
- **THEN** 返回上下文超限类别，不返回原始响应体、密钥或可执行输出，不尝试缩短历史再次请求

#### Scenario: 流中上下文超限

- **WHEN** SSE error 或失败响应明确携带 context_length_exceeded
- **THEN** 返回同一上下文超限类别，部分文本不作为完整响应，历史由调用方保留

#### Scenario: 普通坏请求

- **WHEN** 服务返回 HTTP 400 但没有明确上下文超限错误码
- **THEN** 保持普通 HTTP/远程错误分类，不声称清空历史一定能解决该问题

### Requirement: 协议参数归属

认证密钥、HTTP 请求参数、SSE 事件名、远程响应 ID 以及 Provider 特有续传编码 SHALL 由适配器承担。内核不得根据这些协议细节选择工具、决定授权或维护用户交互状态；v1 无需自动能力探测、多协议配置或会话中热切换 Provider。

#### Scenario: 同一会话结构可用于其他协议

- **WHEN** 后续新增一个满足通用契约的适配器并在启动组装时替换模型实现
- **THEN** 原有工具接口、授权行为和终端事件语义保持不变；不要求已有 Responses 会话历史跨协议迁移
