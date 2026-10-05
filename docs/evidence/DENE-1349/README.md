## DENE-1349 一次性 CLI 重启续接插话：实测记录

- 时间：2026-10-05，本机 `cursor-agent 2026.09.18-9a7762b`。
- 做法：`cursor-steer-probe.go.txt` 用 daemon 同一条路径（`agent.ResolveBackend("cursor")` 外包 `agent.WithRestartSteer`）跑真 CLI。
  原任务是逐条执行 5 个 `sleep 10 && echo step N > stepN.txt`。`step1.txt` 出现后发插话：「补充：在做完原来的步骤之后，再建一个文件 extra.txt，内容是 STEERED。然后继续原任务，最后回复 ALL DONE。」
- 原始事件流：`cursor-steer-run.jsonl`（工作目录已替换成 `<workdir>`）。

| 检查 | 结果 |
| --- | --- |
| 插话前的会话 ID | `f32c5db4-5f66-4a50-8035-44401bc0d4cf` |
| 插话后、运行结束时的会话 ID | `f32c5db4-5f66-4a50-8035-44401bc0d4cf`（不变） |
| 插话回执 | 送达（`Supplement` 返回 nil，约 15ms 内新进程已起） |
| 模型读到补充 | 新进程第一句：“Step 1 got interrupted, so I'm checking whether `step1.txt` was written.” 随后建 `extra.txt`（内容 `STEERED`） |
| 接着干原任务 | 继续 step 2–5，最后回复 `ALL DONE`；运行状态 `completed` |

界面预览：`preview.html`（1280 与 390 截图同目录）。
