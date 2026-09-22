# Projects and resources

A project groups work and carries durable resources. A resource is not just
display metadata; it is context later injected into task briefs and
`.multica/project/resources.json`.

- [Core model](#core-model)
- [CLI](#cli)
- [local_directory execution modes](#local_directory-execution-modes)
- [Referring to a project in a comment](#referring-to-a-project-in-a-comment)
- [When to add a resource](#when-to-add-a-resource)
- [Debugging wrong context](#debugging-wrong-context)
- [Side effects](#side-effects)

## Core model

Projects are durable context containers. Resources attached to a project can
affect future agent tasks.

```bash
multica project list --output json
multica project get <project-id> --output json
multica project resource list <project-id> --output json
```

Project resources are mutated through project resource commands/endpoints. Issue
comments do not create durable project resources.

A project's `description` is also durable context: when an issue (or a
quick-create task) is bound to a project, the project description is injected
into the agent's brief under `## Project Context` and written to
`.multica/project/resources.json` as that project's `project_description`. Use
it for project-wide rules/context that should apply to every task in the
project.

A Chat can attach SEVERAL projects at once (a 2–3 project comparison or a
cross-repo task is the common case). The brief then aggregates every attached
project — one `### Project: <title>` subsection each with its own description
and resources, plus the union of their `github_repo` resources in the
Repositories list — and `.multica/project/resources.json` carries one entry per
project under `projects[]`. Two rules follow from that:

- No single project is authoritative in a multi-project Chat. When a
  deliverable must belong to one project (creating an issue, for example),
  infer the target from the request and the descriptions; ask the user when it
  stays ambiguous.
- `multica repo checkout` serves every attached project's repositories, so a
  cross-repo task needs no extra setup. A URL attached to two projects appears
  once.

Common resource types:

- `github_repo` — durable GitHub repo context, with `resource_ref.url`, optional
  checkout `ref`, optional prompt-only `default_branch_hint`, and `repo_key`
  (the same normalized `host/owner/name` a local checkout uses, computed from
  the URL on save);
- `local_directory` — daemon-local path context, with `resource_ref.local_path`,
  `daemon_id`, optional label, and optional `execution_mode` (`in_place`, the
  default, `worktree`, or `shared`). It may also carry `real_path` (the
  symlink-resolved path, which is the directory's identity), `repo_key` (the
  normalized identity of the repository it holds, empty when there is none),
  `worktree_root` (where parallel mode puts working copies) and `is_git_repo`.
  Parallel (`worktree`) mode requires `is_git_repo: true` at save — a git
  working tree with at least one commit. A client that cannot look at the
  disk (the web UI) must not save that mode. `worktree_root` must be absolute,
  must not sit inside the bound directory, and must not overlap another
  bound directory on the same machine.

A project may hold SEVERAL `local_directory` resources on one machine — four
unrelated plain folders, or a repository plus its docs checkout. Two rules,
enforced by the database, bound that:

- one row per directory per machine, keyed on `real_path` (falling back to
  `local_path` for rows written before that field existed);
- one row per repository per machine, keyed on `repo_key` when it is non-empty.
  An empty `repo_key` means "unidentifiable" and never collides, which is what
  keeps plain folders legal.

A run writes exactly ONE directory: the first `local_directory` for that
machine in `position` order. The rest reach the agent read-only and are listed
in the brief's `## Code Source`. Reorder the resources to change which one a
run writes.

## CLI

```bash
multica project list --output json
multica project get <project-id> --output json
multica project create --title "<title>" --repo <github-url> --output json
multica project create --title "<title>" --start-date 2026-03-01 --due-date 2026-03-31 --output json
multica project update <project-id> --title "<title>" --output json
multica project update <project-id> --due-date 2026-04-15 --output json
multica project update <project-id> --start-date "" --output json   # clear the start date
multica project status <project-id> in_progress --output json
multica project resource list <project-id> --output json
multica project resource add <project-id> --type github_repo --url <github-url> --output json
multica project resource add <project-id> --type github_repo --url <github-url> --ref <branch-or-sha> --output json
multica project resource add <project-id> --type local_directory --local-path <abs-path> --daemon-id <daemon-id> --output json
multica project resource add <project-id> --type local_directory --local-path <abs-path> --daemon-id <daemon-id> --execution-mode worktree --output json
multica project resource update <project-id> <resource-id> --execution-mode in_place --output json
multica project resource update <project-id> <resource-id> --execution-mode shared --output json
multica project resource update <project-id> <resource-id> --url <new-github-url> --output json
multica project resource update <project-id> <resource-id> --ref <branch-or-sha> --output json
multica project resource remove <project-id> <resource-id> --output json
```

For `github_repo`, non-JSON `--ref` sets `resource_ref.ref`, the default
checkout branch/tag/SHA for future tasks in that project. JSON `--ref '<json>'`
remains the escape hatch for full payloads or resource types not covered by
shortcuts. `project resource update` merges shortcut edits with the existing
`resource_ref`, so a partial edit does not clobber required fields — including
`real_path`, `repo_key`, and `worktree_root`. On this machine the CLI also
measures those identity fields on `resource add` and sends them.

`--start-date` / `--due-date` are optional calendar days (`YYYY-MM-DD`, like
issue dates). On `project update`, pass an empty string (`--start-date ""`) to
clear a date; an unset flag leaves it untouched.

## local_directory execution modes

`--execution-mode` decides how tasks share a `local_directory`.

`in_place` (default) runs the agent in the user's directory, one task at a time;
a second task waits in `waiting_local_directory`.

`worktree` gives each task its own git worktree of that repo, so tasks run
concurrently and each delivers its work as a branch in the user's repo instead
of editing the working copy. The working copy is created on the USER's disk,
beside their repository — `<repo>.multica-worktrees/<task>` by default, or
under `resource_ref.worktree_root` when set. It is never inside the repository
working tree (it would show up in their `git status`) and never inside the
Multica workspace (the workspace GC would reclaim the one place a failed run's
state can be inspected). A daemon that does not advertise
`local-worktree-user-root-v1` does not receive `worktree_root` at all and keeps
its older behaviour.

Working copies are removed when a task finishes cleanly. One that survives is
one a run could NOT finish — an unresolved merge, a failed commit, a dead
daemon — and it stays on disk on purpose. Automatic cleanup of those is a
machine-level setting, OFF by default (Desktop → daemon settings). When on, a
copy is removed only if ALL of: Multica created it, no task is in it, its last
run is older than the configured window (default 14 days), `git status
--porcelain` is empty, and its work is already in trunk — the branch is
contained in trunk, or its changes were squashed into it (compared by content,
since a squashed branch is never an ancestor of trunk). A deleted remote branch
and a merged pull request are deliberately not accepted as evidence. Any one
condition failing keeps the copy, and the settings screen shows which. Removal always goes
through `git worktree remove`.

Parallel mode is never preselected when a directory is added: a new
`local_directory` defaults to `in_place`, and moving to `worktree` is an
explicit choice, because its cost is a working copy per task on the user's own
drive. A directory proven not to be a git repository (`is_git_repo: false`) is
refused in `worktree` mode at save time. Every task of one conversation shares that branch —
`agent/<agent>/<issue>` for an issue, `agent/<agent>/chat-<session>` for a chat
— and each turn's worktree starts from the previous turn's work rather than from
`HEAD`; a task with no conversation behind it gets `agent/<agent>/<task>`.

Continuation is decided by an ownership record
(`refs/multica/local-state/<branch>`, which holds the owning conversation, the
snapshot of the user's directory the branch already carries, and the branch tip
it was recorded at), never by the branch name. A same-named branch the user
created — or one that no longer contains the recorded commit, i.e. deleted and
recreated or force-moved — is left alone and the task falls back to
`agent/<agent>/<issue>-<id>`.

A turn replays only what the user changed since that snapshot; when those edits
conflict with the branch's own work the worktree is handed to the agent
mid-merge and the run delivers nothing until the agent resolves it.

`shared` runs the agent in the user's directory like `in_place`, but without
the per-directory lock: tasks on the directory run concurrently, and Multica
keeps its own per-task files (task marker, `resources.json`, skills, runtime
brief) in the task's env root instead of the directory. Use it for a directory
that is a container of several repositories, each with its own branch
worktrees, where tasks already isolate themselves by convention and the lock
only serialised them. Nothing protects two tasks that edit the same checkout at
once — that is the trade the mode makes. The directory need not be a git
repository. Not every runtime can run it yet: a task whose runtime has no
sidecar-free route for the brief fails with a message naming the runtime
(Claude Code, Codex, DSH, OpenCode via `OPENCODE_CONFIG_DIR`, Cursor via `--add-dir` plus an inline brief, Antigravity via `--add-dir`, and the other inline-brief runtimes are supported).

When to pick `shared` rather than `worktree` or `in_place`:

- The path is an umbrella directory (several git repos, each already using
  linked worktrees per task/branch), not one working copy. `worktree` mode
  needs the *resource path itself* to be a git repository and will fail
  otherwise.
- Tasks already isolate their writes by workspace convention (checkout a
  per-task worktree under each sub-repo). Multica will not create those
  worktrees, will not lock the umbrella path, and will not write sidecar
  files into it.
- You can accept two tasks colliding if they both edit the same checkout.
  If they cannot, stay on `in_place` (serial) or bind a single repo as
  `worktree`.

The save-time and claim-time gates for `shared` are the `local-shared-v1`
capability. An older daemon that does not advertise it would json-skip
`execution_mode`, take the path mutex, and silently re-serialise the
directory — the server refuses the save (HTTP 422, code
`daemon_version_unsupported`) and cancels a claim instead. The frontend
`local_worktree_supported` config flag is the "this server validates
`execution_mode` at all" signal for both gated modes; a server that predates
it would drop `shared` the same way it dropped `worktree`.

`worktree` requires the path to be a git repository with at least one commit;
tasks fail with an explicit error otherwise. The gate is the `local-worktree-v1`
capability the daemon advertises — not its version string — and it is checked
twice: at save time, and again against the daemon that claims each task, so a
machine whose runtime cannot do worktrees gets its tasks cancelled rather than
run in place. Saving `worktree` is refused (HTTP 422, code
`daemon_version_unsupported`) while the daemon on that machine does not
advertise the capability — the fix is updating the Multica app there, then
retrying. Pass an empty value to clear it back to the default.

## Referring to a project in a comment

A project has no `MUL-123`-style identifier, so writing its title as prose
produces dead text — there is nothing for the reader's client to autolink. Use
the mention-link form instead, with the project UUID from
`multica project list --output json`:

    [Roadmap](mention://project/<project-id>)

Every client makes it navigable, with different presentation: web and desktop
render a chip carrying the project's icon and current title, while mobile
renders an ordinary link that opens the project on tap. Unlike `@agent` /
`@squad`, it is a pure link: the mention parser does not recognize `project` at
all, so it enqueues nothing and notifies nobody — the same no-side-effect
contract as an `issue` mention.

Prefer this form over pasting the project's URL. Web and desktop do unfurl a
bare in-app project URL into that same chip, but mobile does not — there a
pasted URL is handed to the system browser and takes the reader out of the app.

## A local directory wins over a remote repo

When a project carries a `local_directory` resource on the machine running a
task, that directory IS the task's code. This is a rule, not a preference:
`multica repo checkout <url>` returns the local path instead of cloning
whenever a git remote in that directory (or in a subdirectory one level down,
for `shared` umbrella directories) resolves to the same repository. The run's
brief says so in its `## Code Source` section, listing which repositories are
already present and which still need a checkout.

`github_repo` is not deprecated by this — CI, cloud runners, and teammates with
no local checkout all still need it, and a project pinned to one directory can
reference other repositories that check out normally.

Two consequences worth knowing before debugging:

- A directory that carries the repository's NAME but no matching git remote
  makes the checkout fail with HTTP 409 and an explanation. It does not fall
  back to cloning: a silent fallback is what put two copies of one repository
  on the same machine. Fix the directory or remove the resource.
- Which directory you get depends on the resource's `execution_mode`. In
  `in_place` and `shared` it is the user's own checkout: it may carry
  uncommitted work, and nothing there was reset. In `worktree` it is this
  task's private worktree of that repository, not the user's copy — commit
  there and deliver a branch. A repository that lives beside the pinned
  directory but has no counterpart inside the worktree is refused rather than
  answered with the user's path.
- A repository configured both ways shows a duplicate warning in the project's
  resource list with a one-click merge that removes the redundant `github_repo`
  rows. Nothing is removed automatically — the server compares a URL against a
  path and can only match by repository name, which is enough to ask and not
  enough to act.

## When to add a resource

Add/update a project resource when the user asks for durable project context:
"把这个 GitHub repo 绑到项目上", "以后都用这个 repo", "agent 总是拿不到这个项目的
仓库", or "这个项目要在我的本地目录里跑".

Project resources are durable and affect future tasks. `multica repo checkout`
is task-local checkout state.

## Debugging wrong context

1. `multica project get <project-id> --output json`.
2. `multica project resource list <project-id> --output json`.
3. Check `github_repo.resource_ref.url`, optional `ref`, `default_branch_hint`,
   and `local_directory.resource_ref.daemon_id`.
4. Updating resources is a durable mutation. After an update, listing the
   resource is the verification path.
5. If resources match the expected task context, inspect runtime/repo checkout
   path next.

## Side effects

Project create/update/delete/status and project resource add/update/remove
mutate durable workspace state and affect future tasks. Ask before changing
`local_directory` unless the user explicitly requested that exact local path.
