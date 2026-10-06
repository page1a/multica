## DENE-1347 ACP 类 CLI 停一步续接：实测记录

做法：插话时先给当前 prompt 发 `session/cancel`，等它停下，再在同一进程、同一 sessionId 里发一条 `session/prompt`（开头说明「这一步是有意停下的，接着做原任务」+ 补充内容）。被打断那一轮的结果被吸收，只把最后一轮交给运行结果。

| CLI | 状态 | 说明 |
| --- | --- | --- |
| Grok 1.0.41（借用同一段代码直接跑 ACP 协议） | 实测通过 | 见下 |
| Kimi | 未实测 | 本机 Kimi 没登录；Kun 10-05 决定不实测，靠单测和 Grok 实测覆盖 |
| Hermes | 未实测 | 本机安装已坏；同上，不实测 |
| Kiro / Qoder / QwenPaw / Reasonix / Trae / ZeroClaw / Devin / DIM / MCode | 未实测 | 本机没装；与 Kimi/Hermes 共用同一段代码，单测覆盖 |

### Grok 原始协议实测（`TestACPHandoffSteerRawProtocol`，96s PASS）

- 原任务：跑 20 秒的 `tick` 循环，把最后数字写进 `done.txt`。
- 工具开始 3 秒后插话：「再建一个 banana.txt 写 BANANA，回复以 BANANA 结尾」。
- 发出 `session/cancel` → CLI 回 `stopReason:"cancelled"`（MidTurnAbort）。
- 2ms 后同一 sessionId 发出续接 prompt；第二轮以 `end_turn` 结束。
- 运行侧只看到 `[end_turn]`，被打断那一轮没有被当成失败。
- 结果：`done.txt = "20"`（原任务完成），`banana.txt` 含 BANANA，回复以 BANANA 结尾（补充被读到）。

### 复现命令

```bash
cd server
# Kimi 登录后：
MULTICA_RUN_REAL_AGENT_SMOKE=1 MULTICA_STEER_PROVIDER=kimi \
  go test -tags agentintegration -run TestACPHandoffSteerRealSmoke -v ./pkg/agent
# 任意 ACP CLI 原始协议：
MULTICA_RUN_REAL_AGENT_SMOKE=1 \
MULTICA_STEER_RAW_CMD="grok --no-auto-update agent --always-approve stdio" \
MULTICA_STEER_RAW_AUTH=cached_token \
  go test -tags agentintegration -run TestACPHandoffSteerRawProtocol -v ./pkg/agent
```

界面预览：[steer-menu-preview.html](steer-menu-preview.html)（桌面 + 手机 390px）。
