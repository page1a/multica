---
name: multica-platform
description: "asks, open a chat, goals, inbox, project board/report, sub-issues, wakeups, charts, routing, close protocol, stalls, driver, halt, mentions, agents, specialisation, squads, autopilot, projects, runtimes, progress, state card, skill import, transfer, linked workspace, GitHub App. Not product code."
user-invocable: false
allowed-tools: Bash(multica *), Bash(git *), Bash(gh *)
---

# Operating Multica

Your runtime brief owns the per-turn workflow: which issue you are on, when to
comment, what status to write. This skill owns the platform contracts behind
it — what a command actually does, what the server validates, and which writes
have consequences you cannot take back.

Read the invariants below, then open the reference(s) your task actually needs — usually one, sometimes a few. Do not read them all.

## Routing

| Open | When the task is about |
|---|---|
| `references/issues.md` | Issues: PR linking vs close intent, reading a linked PR's state, custom properties, status side effects, who else is running |
| `references/charts.md` | Charts and files in a comment: inline html/mermaid versus an attached file |
| `references/drivers.md` | Who drives an open issue (`driver` in `issue get` / `issue children`), the patrol's rerun-then-escalate, and `multica issue dispose` for one nobody drives |
| `references/run-control.md` | Stopping every run on one issue (halt / resume), the chain budget, the run time limit, and steer / queue / restart for a message to a running reply |
| `references/wakeups.md` | Issue wakeups: events, conditions (`--until-*`), timers, check-ins, runaway protection |
| `references/stall-actions.md` | Automatic stall actions: 24-hour keep announcements, parent auto-close, 7-day undo, and CLI/API commands |
| `references/goals.md` | Task goals: draft a completion line, confirm the human lock, track budget, and finish a goal |
| `references/sub-issues.md` | Sub-issues: todo vs backlog at create time, stages as barrier groups, promoting parked children |
| `references/routing.md` | Automatic routing: which slots it fills at which status, the 验收席 field, who may pick an executor (`--per-quote`, `issue escalate`), domains (`multica domain`, project/issue `--domain`), what an `issue route` result means, why dispatch stopped |
| `references/state-card.md` | Picking up an issue: `multica issue context` (goal, decisions, where it stands, last baton, what changed since you), and writing decisions with `--decision` |
| `references/close-protocol.md` | Closing an issue: the eight `close.*` keys, conclusion / status / next owner / wake decision tables, blocked-close fields, dispatcher stage promotion |
| `references/mentions.md` | Writing a `mention://` link: which types enqueue a run, which are inert, why one silently did nothing |
| `references/agents.md` | Creating, copying or debugging an agent definition: fields, secrets, MCP config, skill binding |
| `references/asks.md` | Option questions raised and answered by agents |
| `references/specialisations.md` | Base roles and specialisations: create by base role + domain, what a child inherits, the two-level cap, runtime following, solidify, the archive guard |
| `references/squads.md` | Squads: leader routing, roster, recording leader activity, why a squad did or did not run |
| `references/autopilots.md` | Autopilots: schedule / webhook / manual triggers, `create_issue` vs `run_only`, why one did not fire |
| `references/projects.md` | Projects and their durable resources (`github_repo`, `local_directory`, worktree mode), and project memory (`check`, `status`, `monitor`, `seat`, `chat sediment`) |
| `references/runtimes.md` | Runtimes, daemons, `repo checkout`, and the task CLI boundary |
| `references/inbox.md` | The user asks about their inbox or what is stuck: `multica inbox board` (optionally `--project`) and the fixed five-part answer |
| `references/project-report.md` | The user asks to hear a project's report (听汇报, 有什么新进展, 上次以来): `multica project report --mark-heard`, the fixed spoken shape with a Mermaid chart, and acting on its buttons |
| `references/project-board.md` | The user asks for the full open-ticket panorama by project: `multica project board` (one or more projects, or the whole workspace) |
| `references/chat-spawn.md` | Opening a chat with another agent from a chat (`multica chat open`): task-or-chat table, limits, refusal codes |
| `references/workspace-links.md` | Reading another workspace's shared projects through a link: `multica workspace link list` / `view`, pending link requests (`list --pending`), what the view contains, why it says link not found; on a managed link, working on the source's issues and autopilots with `--linked <source-slug>` |
| `references/transfer.md` | `multica transfer export` / `import` / `bind-runtimes`, and the kun `/transfer/*` endpoints |
| `references/skill-import.md` | Importing a skill into this workspace from a URL or a local archive |
| `references/github-app.md` | GitHub App identity for this deployment: status, and a setup link a person opens to create the App |

Open what the task needs. A single-domain task usually needs one; a task that
crosses domains needs each domain it touches — creating a squad, assigning it an
issue, then writing a mention needs `squads.md`, `issues.md` and `mentions.md`,
and skipping one of those means acting on a contract you have not read.

What is never right is reading every reference because you are not sure. Each
reference states its own contracts in full and none depends on another, so pick by domain and skip the rest.

## Invariants

These hold across every reference and are not repeated there.

**Read before you write.** Start with the read-only commands the reference you
opened names — most domains have a `list` and a `get` that take `--output json`
and have no side effects. Run those before any mutation. When a command's shape
is unclear, `multica <command> --help` beats guessing at flags.

**A name is not an id.** Mention links, assignment, and every `--*-id` flag take
a real UUID from the matching `list --output json`. Never type a display name
where an id belongs, and never invent a UUID: an id that is well-formed but
belongs to nothing fails in ways that read like a permission error, which sends
you debugging access when the real problem was the id.

**`--output json` writes to stdout; warnings and confirmations go to stderr.**
Do not merge them (`2>&1`) into anything that parses the output — that makes a
write which SUCCEEDED look like it failed, and invites a duplicate retry.

**Long-tail settings:** `multica settings get <key>` and `multica settings set <key> --value-json '<json>'` (secrets via `--value-file` or `--value-stdin`); `repo.shares` revokes with `{"url","member_id","revoke":true}`. The key table is `docs/kun/settings-cli-coverage.md`.

**Writes are real.** Creating, updating, deleting, assigning, commenting,
mentioning, triggering and status changes mutate durable workspace state or
start agent runs that cost real budget. Never run one to see what happens. When
the user has not asked for a specific mutation, propose it instead of making it.

**A chat agent dispatches; it does not do the work.** Past an aligned small fix it
opens a ticket with `## 目标` / `## 验收` (`references/chat-spawn.md`); routing picks unless a person
must do it. Only 「你来做」 (or your name) self-assigns with `--per-quote "<原话>"`; an unverified quote goes to routing.

**The owner of a ticket owns it to the end.** Once it is yours: post a start
comment first (how you read the ask, which direction you will take), comment at
key milestones, and finish only with `multica issue close`. Too hard →
`multica issue escalate`. Someone else should take it → close `blocked` and let
routing advise. A person must decide → `multica issue summon`. **Never assign
the ticket to someone else yourself**; with routing on, the server refuses it
once the ticket is past `todo` (details in `references/routing.md`).

**Status keys identify workflow states; categories describe lifecycle only.**
Custom statuses do not inherit built-in automation behavior. For status side
effects and API field meanings, read `references/issues.md`.

## Chats

Use the read-only chat directory before opening a transcript:

- `multica chat list [--project <id>] [--all-projects] [--since <RFC3339>] [--output json|table]`
- `multica chat search <词> [--project <id>] [--all-projects] [--since <RFC3339>] [--output json|table]`

Task-scoped commands default to the current project. Use `--all-projects` to
opt in to a workspace-wide directory. Visibility follows the person who
started the task, so another member's private chats stay hidden. Listing and
reading chats never changes unread state. Use `multica chat history` for a
bounded transcript after choosing a session.

To message a chat while its agent may still be replying, use
`multica chat send --session <id> --content-file <path> --mode steer|queue|restart`.
The same `--mode` works on `multica issue comment add`, which also takes
`handoff` / `parallel` when you @ an agent other than the one running; a chat
moves to another agent with `multica chat handoff --to <agent>`. What each mode costs,
and what happens when the running CLI cannot steer, is in
`references/run-control.md`.

To promote the current conversation into a goal task, use
`multica chat to-goal --session <id-or-url>`. The server creates the issue with
the chat's agent as executor; confirm the completion line through the shared
`multica goal` commands. Add `--output json` when another tool needs the new
issue id.

To talk to another agent without creating work, open a chat from your chat:
`multica chat open --agent <name> --brief-file ./brief.md`. Work with an owner
and a deliverable is still an issue. Limits and refusal codes:
`references/chat-spawn.md`.

**Catch up with the state card; Comment reads stay bounded.** `multica issue
context <id>` lists the goal, settled decisions, where it stands, the last
handoff and the threads new since your last run; expand one with
`--thread <thread-id> --tail 30`. A wider read scans roots first
(`--roots-only --summary --compact`), never one unbounded pull; when the
per-turn message hands you a `--since` delta, that read is the bounded scan. Rule locations: `references/state-card.md`.

## When behavior looks wrong

Classify before concluding: expected behavior, a configuration problem, a
product limitation, or an actual bug. Explain what the platform currently does
rather than defending it; when the behavior is technically correct but bad for
the user, say so and propose a scoped change.

Do not silently alter routing, briefing, or trigger behavior to make a complaint
go away. Those are product contracts, and changing one without confirmation
moves the surprise to somebody else.


## Progress

Use `multica issue progress` and `multica chat progress` to report a concise current update. At the beginning of a chat run, report a title with `multica chat title "Project · topic"`; a manual member rename is locked and the command returns a reason. For a human-created issue, suggest a clearer first-pass title with `multica issue title <id> --suggest "<title>"`; it never applies the change. See `references/progress.md`.
