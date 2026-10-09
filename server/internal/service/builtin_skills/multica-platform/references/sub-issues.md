# Sub-issues and stages

## Sub-issues: todo starts work now, backlog parks it

On an agent-assigned issue, create status decides whether the assignee fires
immediately. A non-backlog status (e.g. `todo`) enqueues the agent at create
time; `backlog` sets the assignee without triggering.

Routing never picks up `backlog`, so a ticket that should start now goes in
`todo` (the default when `--status` is left out). An agent parking a ticket in
`backlog` names what it waits for with `--waiting-for "<one line>"`; without
it the server refuses and points at `todo`. Staged children (`--stage <N>`)
are exempt — the stage before them is what they wait for. People are not
asked. The line shows on the ticket and in `issue get` as
`metadata["backlog.waiting_for"]`, and is dropped when the ticket leaves
backlog.

Parallel children — all start now:

```bash
multica issue create --title "..." --parent <issue-id> --assignee <agent> --status todo
```

Strictly serial children — park later steps, promote one at a time:

```bash
multica issue create --title "Step 2: ..." --parent <issue-id> --assignee <agent> --status backlog --waiting-for "Step 1 merged"
multica issue status <child-id> todo   # promote when the previous step is truly done
```

Creating every serial step as `todo` enqueues the whole chain at once.

### Stages: order sub-issues into barrier groups

`--stage <N>` (N >= 1) groups sub-issues under the same parent into ordered
stages. The server **tries once to wake the parent assignee when a whole stage
finishes** — i.e. every sub-issue in the lowest unfinished stage has reached a
terminal status (`done`/`cancelled`); a notification that fails is not replayed.
A completion that does not close a stage is silent (no comment, no wake). A
sibling set with **no** stages is one implicit stage, so the parent is woken
once when the *last* sub-issue finishes — not on every child.

Advancement is agent-driven: the server only detects the closed barrier and
wakes the parent assignee, who then decides whether to promote the next stage's
`backlog` sub-issues to `todo`.

```bash
# Stage 1 runs now; later stages parked until promoted
multica issue create --title "Research A" --parent <id> --assignee <agent> --stage 1 --status todo
multica issue create --title "Research B" --parent <id> --assignee <agent> --stage 1 --status todo
multica issue create --title "Build"      --parent <id> --assignee <agent> --stage 2 --status backlog
multica issue create --title "Ship"       --parent <id> --assignee <agent> --stage 3 --status backlog
```

When both Stage 1 sub-issues finish you (the parent assignee) are woken with a
"Stage 1 complete" comment. Inspect the layout, then promote the next stage:

```bash
multica issue children <parent-id>             # sub-issues grouped by stage
multica issue status <stage-2-child-id> todo   # promote when its deps are met
```

`issue children --output json` reports per-stage `done` counts, including custom
statuses in terminal categories. When reading issue JSON, `status` is the exact
key; `status_category` retains the seven-value API enum for installed clients:
`backlog` / `todo` mean unstarted, `in_progress` / `in_review` / `blocked` mean
started, `done` means successful terminal, and `cancelled` means cancelled
terminal (the internal closed category). These values encode lifecycle, not
built-in automation behavior. Check `status_category` for `done` / `cancelled`
(or use the stage counts), not just the concrete `status` key, to recognize
terminal children.

Read each sub-issue's description before promoting and only promote items whose
stated dependencies are met; if a description conflicts with the parent's
breakdown, leave it `backlog` and comment to confirm first.

## One branch, one PR: sub-issues deliver onto the parent's line

A sub-issue created after DENE-1537 has a `delivery_line` in `issue get` /
`issue children` JSON and opens **no PR of its own**. Its worktree starts from
the tip of the parent's delivery branch, so it sees every earlier stage's
commits; parallel siblings each get their own worktree from that tip.

- Commit your work; do not push, open a PR, or switch branches.
- `multica issue close <child> --outcome done`, run in the sub-issue's working
  directory, merges its commits into the parent's branch and posts the commit
  list as evidence. Nothing reaches the base branch.
- A real conflict closes the child `blocked` with the files named. Run
  `git merge <parent-branch>` on your branch, resolve, commit, close done again.
- The parent continues that branch and opens the one PR, with
  `Closes DENE-N` for every sub-issue; CI, review and merge happen there.
  `--verdict pass` on a line child is refused; accept on the parent.

`multica issue delivery <parent>` lists each sub-issue's merged commits.
Sub-issues that already had their own branch or PR keep the old flow.

## Incorrect to correct

Serial / phased sub-issues (don't start the whole chain at once):

```bash
# incorrect — all fire immediately, no ordering
multica issue create --title "Step 2" --parent <issue-id> --assignee <agent> --status todo
multica issue create --title "Step 3" --parent <issue-id> --assignee <agent> --status todo

# correct — stage them; Stage 1 runs, later stages park and are promoted as
# each stage's barrier closes
multica issue create --title "Step 1" --parent <issue-id> --assignee <agent> --stage 1 --status todo
multica issue create --title "Step 2" --parent <issue-id> --assignee <agent> --stage 2 --status backlog
multica issue create --title "Step 3" --parent <issue-id> --assignee <agent> --stage 3 --status backlog
```
