# Linked workspaces (read-only)

A link lets this workspace read the status of another workspace's chosen
projects without anyone joining it. The other workspace is the source; this one
is the viewer. Agents only read:

```sh
multica workspace link list --output json
multica workspace link view <link-id> --output json
multica workspace link view <link-id> --project-id <id> --cursor <next_cursor> --output json
```

`list` shows the active links this workspace receives. `view` returns the
source's name, its shared projects with `done`/`total` counts, and one page of
tasks: `identifier`, `title`, `status`, `priority`, `assignee_name`, `due_date`,
`updated_at`. Pass `next_cursor` back as `--cursor` for the next page; it is
empty on the last page. `--project-id` takes an `id` from the view's `projects`.

That is everything the link exposes. Task descriptions, comments, attachments,
private tasks, private projects and projects the source did not tick are never
in the response, and nothing here can write to the source. Do not try to reach
the source through `multica issue` or `--workspace`: you are not its member and
every such call is refused.

Every refusal is the same `404 link not found`: the link was revoked, is still
waiting for acceptance, or your workspace is not its viewer. Report it as "no
access", not as a bug.

Creating, changing projects, accepting and revoking (`create`, `update`,
`revoke`, and the `lookup` that confirms a target) are refused for agents.
When the user wants one, tell them where it lives: 设置 → 连通工作区. The source workspace's owner offers the link and
picks the projects (the target can be pasted as the other workspace's link or
its slug); the viewer's owner or admin accepts it; either side can revoke.
Someone who owns both workspaces can instead pull from the viewer side (设置 →
连通工作区 → 连进来的, or `create --from <workspace>`); that link is active at
once. Anyone else must ask the source's owner to offer it.
