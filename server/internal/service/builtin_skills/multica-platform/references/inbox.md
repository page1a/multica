# The inbox, told back

When the user asks about their inbox, or asks what is stuck, what needs them,
or what happened today (帮我过一遍收件箱, 有什么卡住了, 今天有什么要我处理),
read the board and tell it back in the fixed shape below. Do not piece the
answer together from `issue list`. The board already applies the lane rules
the inbox page uses, so your answer matches what the user sees there.

- [Read the board](#read-the-board)
- [Whose inbox it is](#whose-inbox-it-is)
- [Answer in this shape](#answer-in-this-shape)
- [Acting on a row](#acting-on-a-row)

## Read the board

    multica inbox board --output json

This call is read-only. It never marks anything read, so the page still shows
the user what is new. `--tz <IANA zone>` sets the zone that "done today" is
counted in; the default is the machine's zone. The JSON has six lanes:
`waiting`, `stalled`, `running`, `todo`, `fresh` and `done`. Every issue appears in
exactly one lane. A child can nest under its parent in `children`; `fresh`
never nests.

These row fields carry the answer:

| Field | Meaning |
|---|---|
| `identifier`, `title` | The ticket |
| `reason` | What the user is asked to do (waiting) |
| `before` | What happened before it stopped |
| `from_name` | Who called the user (waiting) |
| `kind` / `stuck_kind` | How it stopped (stalled) |
| `next_name` (`next`) | Who holds the next move. On a running row, who is on it |
| `unread` | Unread inbox rows on it |

### One project only

    multica inbox board --project <project id, id prefix or exact name> --output json

The board narrows to that project's tickets, by the same visibility rules as
the unfiltered one. A project the user cannot see gives an empty board; say
so rather than trying another route. When the prompt names a project (the
page's "让 AI 讲讲这个项目" button does), read the board with `--project` and
tell only that project's tickets, so what you say matches what the user sees.
The five parts below stay the same; open with which project this is.

## Whose inbox it is

A person running the CLI gets their own board. An agent gets the board of
the person who started its run by hand: a chat message, a comment, an @, or
an assignment. That person's visibility applies, so the board shows only what
they can see. A run started by an automation or by another agent has no
person behind it, and the call fails with `inbox_board_no_person`. When that
happens, say so. Do not fall back to another person's inbox.

## Answer in this shape

Use these five parts, in this order, in the user's language. Leave out a part
whose lanes are empty rather than writing "none".

1. **要你对齐** (`waiting`): one line per ticket, giving the identifier and
   title, what they need the user to do (`reason`), and who is waiting
   (`from_name`, else `next_name`).
2. **卡住了** (`stalled`): one line per ticket, giving where it stopped
   (`before`, else `kind`) and who holds the next move (`next_name`).
3. **正在进行** (`running`): a count and the ticket numbers only.
4. **待做** (`todo`): a count and the ticket numbers only. These are tickets
   assigned to the user and still in todo.
5. **其他新动态和今日完成** (`fresh` + `done`): one line in total, with counts
   and ticket numbers.

Keep it short. The user reads this to decide where to look first; the details
are on the tickets.

## Acting on a row

To answer or unblock a ticket from the chat, reply on that ticket:

    multica issue comment add <issue-id> --content-file ./reply.md

Do not change a ticket's status or assignment just because it appears on the
board. Those changes follow the issue workflow, not the inbox.
