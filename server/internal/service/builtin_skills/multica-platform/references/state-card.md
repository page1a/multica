# State card

`multica issue context <id>` prints a few hundred characters the server derives
from what the issue already records (`GET /api/issues/{id}/context`). Read it
when you pick up an issue someone else worked on, before the comment scan; it
does not replace the scan.

| part | where it comes from |
|---|---|
| 目标 | the title, plus the goal's finish line when the issue has a goal |
| 已拍板 | `--decision` on `issue close` / `issue handoff`, and what people add on the issue page |
| 现在在哪 | the status and the latest `close.*` record; a record the status has since moved past is marked stale and its wait dropped |
| 上一棒交代 | whichever is newer: the latest close's summary, or the latest handoff's `--summary` |
| 你上次之后的变化 | threads with comments new since your previous run on this issue (a person: since their last comment), your own comments excluded; titles and thread ids only |

`--since <RFC3339>` overrides the anchor. `--output json` returns the same parts
as fields, with decision ids, plus the rendered `text`.

## Writing it

Nothing new to remember: the card fills from the calls you already make.

- `multica issue close <id> ... --decision "拍板单独建表" --decision "手机端先做网页"`
  — each settled decision is one line, 300 characters at most, up to 30 per issue.
  `--summary` is the close's 上一棒交代.
- `multica issue handoff <id> --to <target> --summary "服务端已合，剩前端" --decision "..."`
  — the summary is 300 characters at most and becomes 上一棒交代 until the next close.
- `multica issue decision add|edit|rm <id> ...` changes the list directly. An agent
  may edit or remove only decisions it wrote; people edit any of them on the
  issue page. A refusal names why — do not work around it.

Write a decision only for something that was actually settled (a choice
between options, a scope cut, a constraint someone confirmed). Progress and
plans belong in `issue progress`, not here.
