## DENE-1348 插话实测记录

每个 CLI 单独测：让模型先跑一个 8–12 秒的命令，命令还在跑时插一句「最后一句话要以 BANANA 结尾」，看三件事：会话 ID 变没变、模型读没读到、当前工作被没被打断。模型统一用 GLM 5.3（智谱 coding plan），测试日期 2026-10-05。

| CLI | 版本 | 会话 ID 不变 | 模型读到 | 被打断 | 实际档位 |
| --- | --- | --- | --- | --- | --- |
| OpenCode | 1.18.34 | 是 | 是，同一轮回复以 BANANA 结尾 | 否，命令跑完后读 | 插话（1 档） |
| Pi | 0.73.1 | 是 | 是，同一轮回复以 BANANA 结尾 | 否，命令跑完后读 | 插话（1 档） |
| Qwen Code | 0.24.4 | 是 | 是，但要等这轮回复结束后才当新一轮处理 | 否 | 降级为排队 |
| CodeBuddy | 2.161.2 | 未测 | 未测 | 未测 | 降级为排队 |
| DSH | 0.1.5-rc.1 | 未测 | 未测 | 未测 | 降级为排队 |

## 怎么接的

- OpenCode 1.x：守护进程随 `opencode run` 带一个自己的小插件（经 `OPENCODE_CONFIG_CONTENT` 加载，和 MCP 配置合在一起，用户自己的插件照常加载）。插话写进本次运行私有的收件目录，插件把它作为一条真的用户消息加进同一会话，OpenCode 在下一步读到；模型开始回应这条消息时插件才回执「已送达」。2.x 改成后台服务、没有这个配置通道，仍走排队。
- Pi：守护进程用 `--extension` 带一个小扩展，把插话交给 Pi 自带的 steer 队列（当前工具跑完、下次调模型前送进去）；这条消息真正进入对话时才回执。
- 两边都是先认领再回执：守护进程撤回和插件认领抢同一个文件改名，认领到的一定等回执，没认领到的一定不会再送进去；这一轮结束还没送进去的，报失败，服务端改成排队，不丢消息。

日志：[`opencode-live.log`](opencode-live.log)、[`pi-live.log`](pi-live.log) 是经守护进程适配层（`Session.Supplement`）跑真 CLI 的输出；[`qwen-live.log`](qwen-live.log) 是 Qwen 的 stream-json 直测。

## 降级原因

- Qwen Code：stream-json 输入的用户消息进一个顺序队列，必须等这轮 `result` 之后才处理（源码 `Session.userMessageQueue`，且不支持 hook 回调），没有中途送进去的通道。日志里第一轮回复没有 BANANA，第二轮才有。
- CodeBuddy：本机没有 CodeBuddy 账号，即使配自定义模型（`models.json`）也报 `Authentication required`，没法实测。它是 Claude Code 分支，大概率能复用 Claude 的 hook 方案，但没实测不开放。
- DSH：DSH 的 agent 本身有 `steer()`，但守护进程和它之间隔着 `@multica-ai/dsh-runtime`（仓库 `multica-ai/dsh-multica-runtime`，只读、没有 fork），它现在只认 `execute` / `cancel` 两种命令。要接需要它先加 `steer` 命令和回执帧并在 `ready` 里声明能力，守护进程再按能力开放。

## 已知边界

- OpenCode / Pi 的插话都在「当前工具跑完、下次调模型前」送进去。模型正在写最后一段回复时到的：Pi 源码在最后一步后还会再查一次 steer 队列，会多跑一步读它；OpenCode 这一情形没单独实测。回复已经整体结束后才到的，报失败转排队。
- OpenCode 极少数情况下，插话刚写进会话这一轮就结束了：消息已经留在会话里，但这一轮没回应，会按失败转排队，下一轮可能看到两次同样的话。
