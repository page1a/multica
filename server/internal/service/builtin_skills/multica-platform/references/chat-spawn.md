# Opening a chat from a chat

`multica chat open` lets a chat agent start a new conversation with another
agent. The server does everything in one transaction: it creates the chat,
posts your brief as the first message (the agent replies on its own), records
which chat it came from, and leaves a card in your chat linking to it.

```bash
multica chat open --agent <name|id> --brief-file ./brief.md \
  [--title "Project · topic"] [--project <id>] [--visibility private|workspace] \
  [--client-key <key>] --output json
```

The new chat belongs to the person who started your run, so it shows up in
their sidebar. Nothing comes back to your chat automatically in this phase:
read the other chat later with `multica chat history --session <id>` if you
need its answer.

## Task or chat

| You need | Use |
|---|---|
| Work with an owner, a deliverable and a close | `multica issue create` (sub-issues for parallel parts) |
| A conversation with another agent: ask, explore, compare options | `multica chat open` |
| Discussion inside a task | an issue comment; split parallel exploration into sub-issues |
| Turn the current chat into tracked work | `multica chat to-goal` |
| See the issues this chat opened, their status, latest move and phase (waiting_you / in_progress / done) | `multica chat tickets [--session <id>] --output json` |

**Chat aligns, a ticket executes.** In chat, only make an aligned small fix: at
most 3 files and 150 lines, no migration, not a server + UI + CLI change.
Anything bigger that is aligned becomes a ticket (`issue create` or `plan
apply`) whose description carries `## 目标` and `## 验收`; the server refuses a
chat ticket without them. Leave the executor empty for agent work (routing
picks); assign the person when a human must do it. Every ticket links back to
the chat: the chat shows it as a ticket card with live status, `multica chat
tickets` lists them, and `multica issue get` names the chat as `source_chat`.

A task run cannot open a chat (`chat_spawn_task_mode`). That is deliberate:
discussion on a task stays in its comments.

## What the server checks

- Only a chat run may call it; a person cannot (`chat_spawn_agent_only`).
- A chat opened this way cannot open another one: depth is fixed at 1.
- Visibility and project can only stay the same or get narrower than the
  current chat. A private chat yields a private chat.
- The target agent must be one the run's originator may invoke.
- The workspace caps how many chats each chat and each run may open (defaults
  5 and 3). An admin changes this on 设置 › 智能体权限 or with
  `multica settings set agent.spawn`.
- Retrying with the same `--client-key` returns the chat already opened
  (`created: false`). The default key hashes agent, title and brief, so an
  identical retry is safe.

## Refusals

A refusal exits non-zero and prints JSON with `code`, `error`, and for budgets
`limit` and `scope`. The same reason is shown to the person in your chat.

| code | Meaning | Do instead |
|---|---|---|
| `chat_spawn_task_mode` | You are in a task run | `multica issue create` or a comment |
| `chat_spawn_depth_exceeded` | This chat was itself opened by an agent | Ask in this chat, or create an issue |
| `chat_spawn_scope_exceeded` | Wider visibility or another project than this chat | Drop the flag or narrow it |
| `agent_spawn_budget_exceeded` | Cap for this chat (`scope: chat`) or this run (`scope: run`) reached | Reuse an open chat; tell the person |
| `agent_spawn_disabled` | The workspace turned this off | Tell the person; do not retry |
| `chat_spawn_agent_only` | Called without a chat run | Run it from a chat run |

The same policy governs creating issues: `agent_spawn_disabled` and
`agent_spawn_budget_exceeded` can also come back from `multica issue create`
and `multica plan apply` when the workspace limits issue creation from a chat
or from a task.

## Receipts from the issues a chat opened

An IM `/issue` command and an alignment also record their chat, like a chat
run's `issue create` / `plan apply` and `chat to-goal`. `multica issue context`
opens with a 来源 line quoting the message the issue answered.

When such an issue enters 待验收, done, blocked or cancelled, the server posts a
receipt card into the chat: status, the close's summary, PRs, knowledge. A
sub-issue of a ticket from the same chat stays quiet; its parent reports, and
the parent's card lists each sub-issue's conclusion, PRs and knowledge
(only sub-issues everyone in the chat may see). A
chat run opens with the newest ten tickets and where each stands; answer "how
is it going" from that list, not from memory. `multica chat tickets` shows the
same result (`summary`, `pull_requests`, `knowledge`) for every ticket.
