# Goals

A goal is one completion line attached to an issue. It does not add an issue
status or a new page. The completion line is a list of checks plus three
cumulative budgets: tokens, runs, and total duration.

## CLI

Use the same server contract from an agent or a human:

```bash
multica goal draft DENE-123 \
  --check 'unit tests pass' --method test \
  --check 'browser evidence attached' --method screenshot \
  --token-budget 50000 --run-budget 6 --duration-budget 3600
multica goal get DENE-123 --output json
multica goal confirm DENE-123
multica goal budget DENE-123 --tokens 10000 --runs 1 --duration 900
multica goal check DENE-123 1 --status passed --evidence 'verification passed'
multica goal finish DENE-123 --status stopped --reason '把这张票的目标模式去掉'
```

`draft` is the single opening action used by all product entry points. It
creates a goal in `draft` state. `confirm` locks the completion
line and moves it to `active`; once locked, an agent cannot change the
checks or budgets. Only a human can edit a locked goal through the product's
human confirmation flow. `budget` appends to the three limits, and `finish`
sets the goal to `achieved` or `stopped` after the server checks the request.
Appending budget to a stopped goal resumes it and queues the next round.

When a person asks to drop goal mode, an agent stops the goal with
`finish --status stopped --reason '<their words>'`. This works on a draft or a
locked goal, including one the brake paused; the issue then carries on as an
ordinary task, and a stopped draft starts its assignee like `confirm` would.
The goal records who stopped it, the person the run acts for, and the reason.
Stopping does not lower the bar: an agent still cannot confirm, rewrite the
line, add budget to a locked goal, or mark it `achieved` — acceptance decides
that.

Every command accepts `--output json` for automation. The server is the source
of truth for validation, permissions, cumulative usage, and evidence; clients
must not reimplement those rules.

After confirmation, an executor records evidence with `goal check`. The check
argument accepts its id or one-based position. Completing a run never lets an
agent declare the issue done: the server increments cumulative usage, writes a
round activity entry, starts another run while checks remain, and moves the
issue to `in_review` only after every check is passed. At 80% budget it adds a
timeline warning; at 100% (or after three rounds without new evidence) it
blocks the issue and opens an option question for the goal owner.
