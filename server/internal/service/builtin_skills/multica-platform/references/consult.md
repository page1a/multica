# Consulting a stronger seat

`multica issue consult` lets a run working an issue ask a strong-tier seat one
question and wait for its advice. You stay the executor: the advisor only
reads, changes no code or issue, and its answer comes back to your terminal.

```bash
multica issue consult <id> --question-file ./ask.md [--wait 15m] [--output json]
multica issue consult <id> --resume <consult-id>   # the wait ran out; the answer is kept
```

## When to ask

| Moment | What to put in the question |
|---|---|
| Before you start | your plan: "here is how I would do it, what am I missing?" |
| Before you deliver | the diff or result: "review this before I close" |
| Stuck between two approaches | both options and what you cannot judge |

A ticket gets a few consults (3 by default; Settings → Agent permissions →
Consult). Do not ask for what you can look up, and do not consult instead of
working. The advisor sees the issue, its state card and your question — not
your working copy — so paste the code, diff or error that matters.

## Who answers

The server picks a seat on the strongest tier of the routing ladder: one that
fits the issue's direction first, then an idle one, then usage headroom. Never
you. The seat's daemon must be online and new enough to run consults.

## Cost

The advisor's tokens and time are added to the issue's goal budget (no run is
counted) and to the advisor seat's own usage. Each consult leaves one line on
the issue timeline — advisor, cost, and on expand the question and the answer.

## Refusals

A refusal comes back at once; the command prints it and exits 0. Carry on with
your own judgment.

| Code | Means |
|---|---|
| `consult_not_a_run` | only a run working this issue can consult; people comment and @mention |
| `consult_disabled` | the workspace switched consults off |
| `consult_limit_reached` | this issue used its consults (`limit` says how many) |
| `consult_no_advisor` | no other seat sits on the strongest tier |
| `consult_advisors_unavailable` | every strong seat is out of quota, offline or busy |

A consult that got no answer (the advisor run failed or was cancelled) ends
`failed` and does not count against the limit.

## Consult or escalate

| You want | Run |
|---|---|
| advice, and keep the ticket | `multica issue consult` |
| a stronger seat to take the ticket over | `multica issue escalate` |
