## Option questions

Use `multica ask` when a run needs a person to choose among explicit options.
The server validates one to four questions with two to six options each, fills
unanswered questions from their recommended option, and wakes the asking agent
after an answer.

```sh
multica ask create --title "Choose the rollout" \
  --questions-file ./questions.json --issue <issue-id> --output json
multica ask list --status open --output json
multica ask get <ask-id> --output json
multica ask answer <ask-id> --answers-file ./answers.json --output json
```

`questions.json` is a JSON array of `{ "text": string, "options": [{
"id": string, "label": string, "recommended": boolean }] }` objects. The
answer file maps question indexes (`"0"`, `"1"`, …) to option ids or the text
entered through the explicit “other” choice.

When a goal reaches its budget or no-progress brake, the server creates the
same `needs_you` ask on the issue with `extend_budget`, `change_goal`, and
`stop` options. Answer it through the normal ask surface; use `multica goal
budget` to append the new limits, which resumes the stopped goal and queues its
next round.

When an acceptance seat fails and no other seat can cover it (DENE-1647), the
server opens one `needs_you` ask on the issue with `review_stuck.retry` (换席位),
`review_stuck.member` (我来验) and `review_stuck.close` (直接关票). The answer is
carried out by the server: retry looks for another acceptance seat, member
makes the answerer the reviewer, close marks the issue done.
