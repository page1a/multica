# Linked workspaces

A link lets this workspace read the status of another workspace's chosen
projects without anyone joining it. The other workspace is the source; this one
is the viewer. Agents read; on a managed link they also work on the source's
issues and autopilots (see below):

```sh
multica workspace link list --output json
multica workspace link view <link-id> --output json
multica workspace link view <link-id> --project-id <id> --cursor <next_cursor> --output json
```

`list` shows the active links this workspace receives. When the person your
task runs as is this workspace's owner or admin, it also shows offers still
waiting for their answer (`status: pending`, names only); `list --pending`
shows just those. `view` returns the
source's name, its shared projects with `done`/`total` counts and their project
context (`description`, `resources` — a repo `url` or a directory `path` — and
`memory_line`), and one page of
tasks: `identifier`, `title`, `status`, `priority`, `assignee_name`, `due_date`,
`updated_at`. Pass `next_cursor` back as `--cursor` for the next page; it is
empty on the last page. `--project-id` takes an `id` from the view's `projects`.

A chat can attach a shared project as a read-only reference (composer + →
项目上下文 → 连通的项目（只读）). Your brief then has a `Read-only Reference
Projects` section with that context, re-checked against the link every run.
Read it only: you may `multica repo checkout` its repositories to read, but
never push to them, and never open a worktree, commit or write in its
directories. A path marked "not on this machine" is the source's; read its
repositories instead. Your working project and code source stay your own.

That is everything a read-only link exposes. Task descriptions, comments,
attachments, private tasks, private projects and projects the source did not
tick are never in the response, and nothing here can write to the source. Do
not try to reach the source through `multica issue` or `--workspace`: you are
not its member and every such call is refused.

## Managed links

When `list` shows a link with `"managed": true`, you may work on the source's
issues and autopilots for the person who started your run, with that person's
rights there (a guest there gets nothing). Add `--linked <source-slug>`:

```sh
multica issue list --linked <source-slug> --output json
multica issue status <ID> in_progress --linked <source-slug>
multica issue comment add <ID> --content-file note.md --linked <source-slug>
multica autopilot list --linked <source-slug> --output json
multica autopilot update <id> --status paused --linked <source-slug>   # active resumes
multica autopilot trigger-update <id> <trigger-id> --cron "0 9 * * *" --linked <source-slug>
```

Covered: issue list/get/search/children/create/update/assign/status, comments
(list, add), labels, custom properties (`issue property list/set/unset`);
autopilot list/get/create/update/delete, run now (`trigger`), runs and
triggers. `multica autopilot linked-changes <id> --linked <source-slug>` lists
the autopilot's writes made this way (who, via which workspace, which agent). Everything else — members, settings, agents,
MCP, the link itself, close/handoff — is refused with 403. The run executes
nowhere new: the work stays in the source, and each change shows there as
"<person> 经 <your workspace>·<you>". A 403 means managed is off, the link is
gone, the person lacks the right there, or the command is not covered; say so,
do not retry another way.

Every refusal of `view` is the same `404 link not found`: the link was revoked, is still
waiting for acceptance, or your workspace is not its viewer. Report it as "no
access", not as a bug.

An offer puts a request in the inbox of every owner and admin of the receiving
workspace; accepting or declining it sends the offering workspace's owners and
admins a receipt, and once the request is answered or withdrawn the inbox
request is archived. If someone asks what is waiting, run `list --pending` and
tell them to answer it in 设置 → 连通工作区; you cannot accept it yourself.

Creating, changing projects, accepting, revoking and switching managed
(`create`, `update`, `revoke`, and the `lookup` that confirms a target) are
refused for agents. The source's owner switches managed with
`multica workspace link update <link-id> --managed on|off`.
When the user wants one, tell them where it lives: 设置 → 连通工作区. The source workspace's owner offers the link and
picks the projects (the target can be pasted as the other workspace's link or
its slug); the viewer's owner or admin accepts it; either side can revoke.
Someone who owns both workspaces can instead pull from the viewer side (设置 →
连通工作区 → 连进来的, or `create --from <workspace>`); that link is active at
once. Anyone else must ask the source's owner to offer it.
