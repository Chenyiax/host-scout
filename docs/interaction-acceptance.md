# 交互优化验收记录

变更：`improve-terminal-interaction`。日期：2026-10-16（Asia/Shanghai）。模型服务使用本地模拟响应和测试密钥；没有对真实远端服务或生产主机运行命令。

| 检查 | 结果 |
| --- | --- |
| Windows amd64：`go test ./...` | 通过 |
| Windows amd64：`go vet ./...` | 通过 |
| `openspec validate improve-terminal-interaction --strict` | 通过 |
| Linux amd64 真实 controlling PTY：`TestLinuxPTY*` | 通过；由 Windows Go 交叉编译测试二进制，在 WSL2 实际运行 |
| Windows 真终端：`TestWindowsTerminalManual` | 通过；中文与组合字符编辑、左右/Home/End/Delete/Backspace、上下历史草稿恢复、逐次命令审批、模型等待时 Ctrl+C 均实际操作 |

## 已验收的用户路径

- Linux PTY 的 `TestLinuxPTYInteractiveMVP` 覆盖提问、模型等待、带模型用途与精确 argv 的审批、执行结果预览、`/evidence` 详情、追问、同批两个命令分别批准与拒绝，以及取消与恢复；仅获批命令执行一次。
- `TestLinuxPTYUnicodeEditingHistoryAndClear` 验证中文与组合字符的屏幕重绘和提交文本一致，历史导航保留草稿，`/clear` 清空输入历史。`TestLinuxPTYEOFAndHiddenCredential`、`TestLinuxPTYOptionalStepLimit` 和清理失败相关测试验证 EOF、默认拒绝、超限跳过与残留进程提示。
- Windows 真终端手动输入 `ab中é` 后使用方向键、Home/End、Delete/Backspace，模型收到 `bX中`；输入草稿后上下导航，模型收到原草稿。批准 `cmd /c echo approved` 后显示用途、argv、输出预览、退出码和记录 ID；模型等待中 Ctrl+C 立即停止本轮。
- 管道模式测试验证无 ANSI 控制序列、后续聊天行按顺序归属、命令审批默认拒绝且不会消费下一条聊天输入。

## 平台边界

Windows/macOS 的命令进程树收尾仍是开发模式，仅保证直接进程清理；Linux 进程组收尾由 Linux PTY 与进程测试验收。终端宽度、字体和复杂 emoji 的显示列宽随终端实现可能不同，本次在 Windows 真终端和 Linux PTY 验证了中文、宽字符与组合字符的常见路径。
