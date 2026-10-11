# v1 Spec 场景覆盖

对应 `v1-interactive-agent-mvp` 的四份 delta specs，共 110 个 Scenario。每行列出可复现的测试证据；H、N 中涉及 Linux 真实进程和终端的用例已在 WSL2 Linux 实际执行，I 中的清理故障注入测试也已执行。环境与未验证项见 [验收记录](v1-acceptance.md)。

## 证据索引

| ID | 测试或人工验证 |
| --- | --- |
| A1 | [TestGenericClientTwoTurnsAndToolRoundTrip](../internal/agent/contract_test.go) |
| A2 | [TestBusyClearAndImmutableSnapshots](../internal/agent/contract_test.go) |
| A3 | [TestFailedModelHistoryCheckpoints](../internal/agent/contract_test.go) |
| A4 | [TestBatchValidationBeforeCommitOrExecution](../internal/agent/contract_test.go) |
| A5 | [TestApprovalAndOrdinaryFailureEvents](../internal/agent/contract_test.go) |
| A6 | [TestUnknownInvalidAndEmptyResponse](../internal/agent/contract_test.go) |
| A7 | [TestStepLimitAndCancellationBalancePendingCalls](../internal/agent/contract_test.go) |
| A8 | [TestOptionalStepLimitAndLongSequentialTurn](../internal/agent/contract_test.go) |
| A9 | [TestProviderRetriesCountAsOneAgentStep](../internal/agent/contract_test.go) |
| A10 | [TestPostToolModelRetryDoesNotReplayTool](../internal/agent/contract_test.go) |
| A11 | [TestCleanupFailureStopsTurnAndPairsRemainingCalls](../internal/agent/contract_test.go) |
| A12 | [TestContextLimitAfterToolPreservesHistoryUntilExplicitClear](../internal/agent/contract_test.go) |
| A13 | [TestCancelWhileSecondApprovalWaits](../internal/agent/contract_test.go) |
| B1 | [TestCancellationDuringApprovalAndCommand](../internal/agent/cancel_integration_test.go) |
| B2 | [TestCancellationDuringModelRetryStopsBeforeAnotherRequest](../internal/agent/cancel_integration_test.go) |
| C1 | [TestResponsesToolRoundTrip](../internal/agent/session_test.go) |
| C2 | [TestResponsesMultipleCallsReplayAllOutputItems](../internal/agent/session_test.go) |
| D1 | [TestSSEFramingAndTerminalBoundary](../internal/providers/openairesponse/contract_test.go) |
| D2 | [TestSSEProtocolBoundaries](../internal/providers/openairesponse/contract_test.go) |
| D3 | [TestItemOwnershipAndRawReplay](../internal/providers/openairesponse/contract_test.go) |
| D4 | [TestHTTPContractAndErrors](../internal/providers/openairesponse/contract_test.go) |
| D5 | [TestHTTPCancelAndCompletionWithoutEOF](../internal/providers/openairesponse/contract_test.go) |
| E1 | [TestRetryUsesSameRequestAndStopsAfterThirdAttempt](../internal/providers/openairesponse/retry_test.go) |
| E2 | [TestRetryAfterAndPermanentFailures](../internal/providers/openairesponse/retry_test.go) |
| E3 | [TestRetryBeforeDataAndNeverAfterData](../internal/providers/openairesponse/retry_test.go) |
| E4 | [TestRetryWaitCancellation](../internal/providers/openairesponse/retry_test.go) |
| E5 | [TestRetrySharesOneCallDeadline](../internal/providers/openairesponse/retry_test.go) |
| E6 | [TestRequestSendsFullHistoryWithoutCompactionOrTruncation](../internal/providers/openairesponse/retry_test.go) |
| F1 | [TestDecodeStreamKeepsReasoningAndFunctionCall](../internal/providers/openairesponse/client_test.go) |
| F2 | [TestContextLimitUsesOnlyExplicitBoundedErrorCode](../internal/providers/openairesponse/client_test.go) |
| F3 | [TestDecodeStreamRejectsIncompleteAndFailedResponses](../internal/providers/openairesponse/client_test.go) |
| G1 | [TestDeniedCommandDoesNotRun](../internal/tools/command/command_test.go) |
| G2 | [TestInvalidArgumentsAreRejectedBeforeApproval](../internal/tools/command/command_test.go) |
| G3 | [TestStructuredCommandResults](../internal/tools/command/command_test.go) |
| G4 | [TestTimeoutCancellationAndStartBoundary](../internal/tools/command/command_test.go) |
| G5 | [TestCommandEnvironmentOmitsCredentials](../internal/tools/command/command_test.go) |
| H1 | [TestLinuxCancellationKillsProcessGroup](../internal/tools/command/process_linux_test.go) |
| H2 | [TestLinuxNormalExitCleansDescendantsAndPipes](../internal/tools/command/ownership_linux_test.go) |
| H3 | [TestLinuxOwnershipDoesNotKillOtherGroupOrAccumulateFDs](../internal/tools/command/ownership_linux_test.go) |
| H4 | [TestLinuxRepeatedCancellationHasOneResult](../internal/tools/command/ownership_linux_test.go) |
| H5 | [TestLinuxDetachedPipeHolderHasBoundedFailure](../internal/tools/command/ownership_linux_test.go) |
| I1 | [TestCleanupFailurePreservesCompletedOutputAndExit](../internal/tools/command/cleanup_test.go) |
| I2 | [TestCleanupFailureOutranksCancellationAndTimeout](../internal/tools/command/cleanup_test.go) |
| I3 | [TestCleanupFailureWhenDirectWaitDoesNotFinish](../internal/tools/command/cleanup_test.go) |
| I4 | [TestCleanupFailureWhenLivenessCheckExhaustsBudget](../internal/tools/command/cleanup_test.go) |
| J1 | [TestRegistryRejectsInvalidDefinitions](../pkg/tool/tool_test.go) |
| J2 | [TestRegistryDispatchAndSnapshots](../pkg/tool/tool_test.go) |
| K1 | [TestConfigPrecedenceAndValidation](../internal/config/config_test.go) |
| K2 | [TestExplicitTOMLAndPrecedence](../internal/config/config_test.go) |
| K3 | [TestInvalidTOMLDoesNotRevealValues](../internal/config/config_test.go) |
| K4 | [TestRemoteHTTPRequiresExplicitOptIn](../internal/config/config_test.go) |
| L1 | [TestCLIEndToEndHistoryDenialFailureAndClear](../internal/cli/contract_test.go) |
| L2 | [TestCLIStepLimitRecordsAndRecovery](../internal/cli/contract_test.go) |
| L3 | [TestStartupSecretAndErrorSafety](../internal/cli/contract_test.go) |
| L4 | [TestContextLimitDisplayAndExplicitClear](../internal/cli/contract_test.go) |
| L5 | [TestCleanupFailureDisplayNamesOriginalReasonAndRecord](../internal/cli/contract_test.go) |
| L6 | [TestPipeEOFWhileModelRunsCancelsWithoutNextAction](../internal/cli/contract_test.go) |
| L7 | [TestCLIStatusShowsUnlimitedAndSingleTool](../internal/cli/contract_test.go) |
| L8 | [TestCLIStatusShowsEffectiveConfigAndExplicitZero](../internal/cli/contract_test.go) |
| M1 | [TestTerminalCancelAndStaleApproval](../internal/cli/input_test.go) |
| M2 | [TestTerminalInterruptAndEOFAreImmediate](../internal/cli/input_test.go) |
| M3 | [TestPipeInputPreservesLinesAndHasOneConsumer](../internal/cli/input_test.go) |
| M4 | [TestNonTerminalAuthorizationDoesNotConsumeChat](../internal/cli/input_test.go) |
| N1 | [TestLinuxPTYInteractiveMVP](../internal/cli/terminal_linux_test.go) |
| N2 | [TestLinuxPTYEOFAndHiddenCredential](../internal/cli/terminal_linux_test.go) |
| N3 | [TestLinuxPTYLongTurnClarificationRetryAndContextLimit](../internal/cli/terminal_linux_test.go) |
| N4 | [TestLinuxPTYOptionalStepLimit](../internal/cli/terminal_linux_test.go) |
| N5 | [TestLinuxPTYEOFDuringCleanupFailureExitsNonzero](../internal/cli/terminal_linux_test.go) |
| N6 | [TestLinuxPTYCleanupFailurePairsBatchAndStopsModel](../internal/cli/terminal_linux_test.go) |
| O1 | [TestRunStartsConversationOnDevelopmentPlatform](../internal/cli/cli_test.go) |
| J3 | [ExampleNewRegistry](../pkg/tool/example_test.go) |
| J4 | [独立临时 Go 模块导入与注册验证](v1-acceptance.md#公开工具包跨模块验收) |

## 逐场景映射

### agent-runtime

| Scenario | 证据 ID |
| --- | --- |
| 基于上一轮继续提问 | A1, N1 |
| 纯文本回答 | A1 |
| 拒绝并发轮次 | A2 |
| 澄清问题结束当前轮次 | A8, N3 |
| 工具观察后继续回答 | A1, C1 |
| 多个调用顺序执行 | C2, N1 |
| 工具失败可反馈 | A6, A5 |
| 默认超过旧步骤上限继续运行 | A8, N3 |
| 文本与工具共同出现 | A8 |
| 重复标识不产生部分执行 | A4 |
| 历史标识被复用 | A4 |
| 最后一步需要工具 | A7, L2, N4 |
| 最后一步直接回答 | A8 |
| 每轮独立计数 | A8, N4 |
| 网络重试不重开轮次 | A9, A10 |
| 模型流式输出中取消 | N1, D5 |
| 一批工具执行到一半取消 | A13, A7 |
| 运行中的 shell 被取消 | N1, H1 |
| 重试退避中取消 | B2, N3 |
| 取消与清理错误同时发生 | A11, I2, N5 |
| 主进程退出后收尾失败 | I1, H5, N6 |
| 等待命令收尾结果 | A11, B1, N5 |
| 工具之后上下文超限 | A12 |
| 超限后显式清空 | A12, L4 |
| 工具后模型断流 | A3 |
| 首次请求失败仍保留用户消息 | A3 |
| 命令被拒绝 | A5, G1, N1 |
| 回答成功或中断 | A5, B1 |
| 清空后重新提问 | A2, L1 |
| 完成响应没有输出 | A6 |

### model-provider

| Scenario | 证据 ID |
| --- | --- |
| 无 Responses 数据的替代实现 | A1 |
| Provider 续传载荷隔离 | D3, A2 |
| 默认请求行为 | D4, O1 |
| 自定义端点缺少协议支持 | D4 |
| 瞬时失败后成功 | E1 |
| 重试预算耗尽 | E1 |
| 收到流数据后断开 | E3 |
| 重试等待期间取消 | E4, B2 |
| 认证或永久连接错误 | E2 |
| 服务要求较长等待 | E2 |
| 工具之后重试模型 | A10 |
| 参数尚未完成 | D1, D2 |
| 流式完成 | D1, L1 |
| 完成后连接仍未关闭 | D1, D5 |
| 心跳和未知事件 | D1 |
| DONE 之前缺少完成事件 | D2 |
| data 载荷超限 | D2 |
| reasoning 与多个工具调用 | C2, D3 |
| 返回加密续传字段 | F1, D3 |
| incomplete 响应 | F3 |
| 调用方取消连接 | D5, N1 |
| HTTP 上下文超限 | F2 |
| 流中上下文超限 | F2, L4 |
| 普通坏请求 | F2 |
| 同一会话结构可用于其他协议 | A1 |

### host-tools

| Scenario | 证据 ID |
| --- | --- |
| Linux 工具发现 | O1, N1 |
| 非 Linux 开发模式 | O1 |
| 外部模块实现工具 | J4 |
| 扩展工具复用运行时 | J2, A1 |
| 注册集合无效 | J1 |
| 非法命令参数 | G2 |
| 未注册工具 | A6 |
| 拒绝不执行 | G1, N1 |
| 多命令分别确认 | N1 |
| 正常退出后留下同组后代 | H2 |
| 多次取消同一次执行 | H4 |
| 无跨调用后台会话 | H3 |
| 非零退出 | G3, H2 |
| 输出过大 | G3 |
| 凭据环境隔离 | G5 |
| Linux 子进程未自行退出 | H1 |
| 命令超时 | H1 |
| 显式 shell 的子进程清理 | H1, N1 |
| 开发平台取消 | G4, B1 |
| 后代持有输出管道 | H2, H5 |
| 取消时终止请求失败 | I2 |
| 成功退出后清理失败 | I1, N6 |
| 超时收尾未完成 | I2, I4 |
| 成功结果可回看 | L1, N1 |
| 未执行记录可区分 | L2, N4, N6 |
| 清空与退出 | L1, N1 |

### terminal-session

| Scenario | 证据 ID |
| --- | --- |
| 参数覆盖环境变量 | K1, L8 |
| 非安全远程端点 | K4 |
| URL 含凭据或查询片段 | K1 |
| 未配置限制 | L7 |
| 文件设置上限 | K2, L8 |
| 显式解除文件限制 | K2, L8 |
| 环境变量覆盖文件模型 | K2 |
| 配置无效 | K3 |
| 大于旧上限的合法限制 | K2 |
| 隐藏输入 | N2 |
| 非终端输入未提供密钥 | L3 |
| 连续提问和澄清 | L1, N1 |
| 回答模型的澄清问题 | N3 |
| 空白输入 | L1 |
| 查看状态与工具 | L7, L8 |
| 查看记录与清空 | L1, N1 |
| 允许一次 | N1 |
| 非确认内容 | M1, N1 |
| 授权等待时取消 | M1, N1 |
| 就绪时取消输入 | N1 |
| 授权等待时关闭输入 | N2 |
| shell 执行期间取消 | N1 |
| 重试等待期间取消 | N3 |
| 执行期间关闭输入 | L6 |
| 模型出错后继续使用 | L1, N1 |
| 上下文超限展示 | L4, N3 |
| 取消后清理失败展示 | L5, N6 |
| EOF 关闭时清理失败 | N5 |
| 文本只显示一次 | L1, N1 |

## 证据解释

- J4 在独立临时 Go 模块中只导入 `host-scout/pkg/tool`，实现 fake Tool、注册并执行，`go test ./...` 通过；临时模块完成后已删除。
- I 类为可注入私有平台操作的故障测试；H 类、N 类使用实际 Linux 进程与 controlling PTY，不以测试桩代替进程清理验收。
- E 类测试使用本地 HTTP 服务和可控退避/时钟；A10 核对工具完成后的请求重试只重发模型输入，不重新执行工具。
- N5/N6 使用主动脱离进程组且持有输出管道的测试后代制造真实 `cleanup_failed`，测试结束后按 PID 清理该后代。
- 未使用真实 API Key 或付费模型。Windows/macOS 真终端、外部 Responses 兼容端点和 race 检测仍未验证。
