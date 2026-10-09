# Hearing a project's report (听汇报)

When the user taps 听汇报, or asks what is new in a project since they last
heard (有什么新进展, 上次以来怎么样了), read the report and tell it back in the
shape below. Do not rebuild the news from `issue list`: the server decides what
counts as new, and the chat's progress bar and the project page count the same
set.

- [Read the report](#read-the-report)
- [Tell it in this shape](#tell-it-in-this-shape)
- [Acting on a button](#acting-on-a-button)

## Read the report

    multica project report --mark-heard --output json

Without a project it reports every project of the current chat; name one (or
several) to pick. `--mark-heard` records the report as heard for the person the
run works for: their cursor moves to `until`, their inbox rows on the covered
issues are read, and your reply gets the report's buttons. The cursor is per
person + project, not per chat — whatever was heard in another chat or on the
project page is not news here. Leave `--mark-heard` off only when the user asks
for a look without settling it.

Each report has `since` / `until`, `last_heard_at` (null the first time; the
report then looks back a week), `counts` and `items`:

| Field | Meaning |
|---|---|
| `identifier`, `title`, `gist` | The ticket and the line it is for |
| `phase` | `done`, `in_progress` or `waiting_you` |
| `from_status` → `status` | How it moved since the last hearing; `opened` when it is new |
| `changed_at` | When it last moved |
| `source_chat` | The chat that opened it; `title` is empty when the user cannot see that chat |
| `latest_summary` | The conclusion line of its latest close or handoff; absent when that carried none |

## Tell it in this shape

No items in any report: answer one sentence (e.g. 「上次之后没有新进展。」) and stop.

Otherwise, per project (only a heading per project when the chat has several):

1. **Sections by source chat.** 「这个聊天」 for items whose `source_chat.id` is
   this chat (MULTICA_CHAT_SESSION_ID), 「来自「<title>」」 for another chat,
   「其他」 for items no chat opened.
2. **One or two sentences per ticket**, conclusion first, identifier kept:
   what it is for, what got done or where it is stuck (`latest_summary` when
   present), what happens next. The voice follows the Requesting User's
   self-description — plain words if they ask for plain words, PRs and
   engineering terms if that is what they read. When it says nothing about
   this, keep to the conclusion and the identifier.
3. **One Mermaid chart** of where things stand — three groups, top to bottom,
   so it fits a phone:

   ```mermaid
   flowchart TB
     subgraph 做完
       A["DENE-1643 参考项目"]
     end
     subgraph 在做
       B["DENE-1665 聊天开单卡"]
     end
     subgraph 等你
       C["DENE-1667 听汇报"]
     end
   ```

   Short labels (identifier + four to eight characters), only the groups that
   have tickets, no links between nodes.
4. **要你做的** — every `waiting_you` item as one line: identifier, what to
   look at or decide. None: 「没有要你做的。」

The buttons under your reply come from the server; do not write them yourself.

## Acting on a button

A button sends its sentence as the user's next message. Act on the named
ticket, recording the user's words:

| Message | Do |
|---|---|
| 「X 看过了，没问题」 | Comment the user's verdict on X. If X is `in_review` waiting on them, close it as accepted per `references/close-protocol.md`; otherwise just record it |
| 「X 要改」 | Ask what to change, then comment it on X and mention X's executor so the work resumes |
| 「X 卡在哪」 | Say what blocks X and what the user must do; record their answer on X as a comment |
| 「X 加急」 | `multica issue update X --priority urgent`, and comment that the user asked |
| 「都知道了」 | Nothing to change; say so in one line |

The same message does the same thing in any chat: act on the ticket, do not
post into the chat the ticket came from.
