# 本机 JEV / TypeSafe 配置

JEV 是 TypeSafe 的 System One 模型，本机通过 `jev` CLI 使用。凭据**只有一份**，住在
`~/.config/jev/runtime.env`（`jev configure` 写入，权限 600）：

```
JEV_API_KEY=apikey_…
JEV_BASE_URL=https://api.typesafe.ai
JEV_MODEL=jev-latest
```

## 两个名字读同一份凭据

同一个 key 有两种读法，历史上只配了前者：

| 谁 | 读哪个变量 | 从哪读 |
| --- | --- | --- |
| `jev` CLI | `JEV_API_KEY` | 自己读 `runtime.env` |
| 官方文档的 curl、Python / JavaScript SDK | `TYPESAFE_API_KEY` | 只读环境变量 |

所以会出现这个让人误判的状态：`jev status` 显示 `available: true`，而文档里那条 curl
返回 `403 Must supply an API key!`——不是 key 坏了，是 `TYPESAFE_API_KEY` 根本没人设。

## 现在谁来设

**agent 任务**：daemon 在组装每个任务的环境变量时读 `runtime.env`，把同一个 key 以
`JEV_API_KEY` 和 `TYPESAFE_API_KEY` 两个名字导出（`server/internal/daemon/jev_env.go`）。
没配过 JEV 的机器上这一步静默跳过，不会把变量设成空串——空串比不存在更糟，SDK 会当作
已配置然后发出 `Bearer `。

这层在 `custom_env` **之前**叠加，所以某个 agent 要指向另一个 TypeSafe 账号，照样可以在
agent 设置里覆盖。

**人的交互 shell**：`~/.zshenv` 里 source 同一个文件并导出 `TYPESAFE_API_KEY`，不要在
shell 配置里再抄一份 key：

```sh
if [ -r "$HOME/.config/jev/runtime.env" ]; then
  set -a; . "$HOME/.config/jev/runtime.env"; set +a
  export TYPESAFE_API_KEY="$JEV_API_KEY"
  export TYPESAFE_BASE_URL="${JEV_BASE_URL:-https://api.typesafe.ai}"
fi
```

## 验一下

```sh
jev status                      # available: true
jev judge --yesno "今天是周一?"   # 退出码 0 / 3 / 4

curl -sS -X POST https://api.typesafe.ai/v1/systemone \
  -H "Authorization: Bearer $TYPESAFE_API_KEY" \
  -H "Content-Type: application/json" \
  -d '{"state":"付款连挂三天了","model":"jev-latest",
       "questions":{"urgency":{"type":"noul","instructions":"这条消息是否表达了紧急?"}}}'
```

请求体的三种 `type` 只有 `noul` / `choice` / `score`（见
<https://docs.typesafe.ai/api>）。写成 `bool` 会拿到
`400 api_usage_error`，和鉴权无关——两种报错长得不像，别混。

## 与服务端路由的关系

服务端的 `route` 模块（`server/internal/routing/`）走的是 OpenAI 兼容的
chat/completions 接口，**不是** `/v1/systemone`。本机这份配置只服务本机的 `jev` CLI 与
SDK 调用，和工作区设置里的「路由」是两条独立的链路。
