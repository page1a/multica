# GitHub App identity

One deployment has one GitHub App. Environment variables win over an App created from Settings. This command does not install the App onto a GitHub account — that is still the connections page, after the App exists.

## CLI

```bash
multica github-app status [--output json|table]
multica github-app setup-link [--org <login>] [--output json|table]
```

`status` reports `source` (`none`, `env`, or `database`), `configured`, `read_only`, `can_create`, and when present the App name and GitHub management URL. No secrets.

`setup-link` is only for a workspace owner, and only while `source` is `none`. It returns `launch_url`. A person who is signed into GitHub opens that URL, confirms with their password, and creates the App. The CLI cannot enter that password. The link works once and expires after 10 minutes; a reused or expired link shows an error page, so mint a new one instead of resending the old one. The desktop app's Settings button opens this same link in the system browser. `--org` creates the App under that organization; omit it to create the App on the signed-in user.

`--output json` is the default. Warnings stay on stderr.

## What the fields mean

| source | What you do |
|---|---|
| `none` and `can_create: true` | Owner can open `launch_url` |
| `none` and `block_reason: not_owner` | Ask a workspace owner. Do not retry the link |
| `none` and `block_reason: public_url_missing` or `secret_unavailable` | The server cannot finish this. Say so; do not invent credentials |
| `env` | Read-only. `setup-link` returns a conflict. Change the server environment, not Settings |
| `database` | Already created. Connect accounts from Settings → Connections. `setup-link` returns a conflict |

After the person creates the App, the server stores it and opens GitHub's install page. One workspace can hold a personal installation and an organization installation as two connections. The same installation cannot be attached to a second workspace.

`scripts/selfhost-github-app.sh` remains the path with no UI. It writes environment variables, which then win over the Settings row. See `docs/kun/github-app-onboarding.md`.
