#!/usr/bin/env bash
# No-UI fallback for a GitHub App manifest (DENE-959 / DENE-981).
# The primary path is Settings → Connections: a workspace owner creates the
# App there, and the server stores it encrypted without a restart. Use this
# script only when there is no UI. The manifest redirect_url must land on
# /{workspace}/settings?tab=git-connections so the one-hour code is in the
# address bar. Credentials written here go into .env and override the
# Settings row until the variables are removed.
# Steps:
#   1. exchange the one-hour manifest `code` for the App credentials,
#   2. write GITHUB_APP_SLUG / GITHUB_WEBHOOK_SECRET / GITHUB_APP_ID /
#      GITHUB_APP_PRIVATE_KEY into the server's .env (backup kept alongside),
#   3. recreate the backend container so it reads them,
#   4. prove it: the webhook endpoint must answer 401 (configured, bad
#      signature) instead of 404 (unconfigured).
#
# Secrets never touch stdout or this checkout: they flow from `gh api` into a
# 0600 temp file, over ssh stdin, and into the server's .env.
#
# Usage: scripts/selfhost-github-app.sh <manifest-code>
# Env:   SSH_HOST (default ai-multica), REMOTE_DIR (default /opt/multica),
#        PUBLIC_URL (default https://ai.ferryway.cc)
# See docs/kun/github-app-onboarding.md for where the code comes from.
set -euo pipefail

code="${1:?usage: $0 <manifest-code>}"
ssh_host="${SSH_HOST:-ai-multica}"
remote_dir="${REMOTE_DIR:-/opt/multica}"
public_url="${PUBLIC_URL:-https://ai.ferryway.cc}"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
umask 077

gh api -X POST "/app-manifests/${code}/conversions" >"$tmp/app.json"

python3 - "$tmp/app.json" "$tmp/github.env" <<'PY'
import json, sys
app = json.load(open(sys.argv[1]))
for key in ("slug", "id", "webhook_secret", "pem"):
    if not app.get(key):
        sys.exit(f"manifest conversion is missing {key}")
pem = app["pem"].strip()
with open(sys.argv[2], "w") as f:
    f.write(f'GITHUB_APP_SLUG={app["slug"]}\n')
    f.write(f'GITHUB_WEBHOOK_SECRET={app["webhook_secret"]}\n')
    f.write(f'GITHUB_APP_ID={app["id"]}\n')
    f.write(f'GITHUB_APP_PRIVATE_KEY="{pem}"\n')
print(f'App created: https://github.com/apps/{app["slug"]} (id {app["id"]})')
PY

# Replace the four assignments in place (keeping their position next to the
# comments that document them), drop the body of any old multi-line PEM value,
# and append whatever slot was missing.
cat >"$tmp/update_env.py" <<'PY'
import re, sys

env_path, fragment_path = sys.argv[1], sys.argv[2]
KEYS = ("GITHUB_APP_SLUG", "GITHUB_WEBHOOK_SECRET", "GITHUB_APP_ID", "GITHUB_APP_PRIVATE_KEY")


def opens_multiline(value):
    return value.startswith('"') and not (len(value) > 1 and value.endswith('"'))


def blocks(lines):
    """Yield (key_or_None, [lines]) keeping quoted multi-line values together."""
    i = 0
    while i < len(lines):
        m = re.match(r"^([A-Z0-9_]+)=(.*)$", lines[i])
        block = [lines[i]]
        if m and opens_multiline(m.group(2)):
            while i + 1 < len(lines):
                i += 1
                block.append(lines[i])
                if lines[i].endswith('"'):
                    break
        yield (m.group(1) if m else None), block
        i += 1


fresh = {k: b for k, b in blocks(open(fragment_path).read().splitlines())}
out = []
for key, block in blocks(open(env_path).read().splitlines()):
    if key in KEYS:
        out.extend(fresh.pop(key, []))
    else:
        out.extend(block)
for block in fresh.values():
    out.extend(block)
with open(env_path, "w") as f:
    f.write("\n".join(out) + "\n")
PY

remote_tmp="$(ssh -o BatchMode=yes "$ssh_host" 'umask 077; mktemp -d')"
scp -q -o BatchMode=yes "$tmp/github.env" "$tmp/update_env.py" "$ssh_host:$remote_tmp/"
ssh -o BatchMode=yes "$ssh_host" "cd '$remote_dir' && cp -p .env .env.bak.github-app.\$(date +%s) \
  && python3 '$remote_tmp/update_env.py' .env '$remote_tmp/github.env'; rc=\$?; rm -rf '$remote_tmp'; exit \$rc"

echo "Recreating backend on $ssh_host…"
ssh -o BatchMode=yes "$ssh_host" "cd '$remote_dir' && sudo -n systemctl stop multica-autoupdate.timer 2>/dev/null || systemctl stop multica-autoupdate.timer 2>/dev/null || true; \
  docker compose -f docker-compose.selfhost.yml -f docker-compose.selfhost.build.yml up -d --no-build --force-recreate backend; \
  rc=\$?; sudo -n systemctl start multica-autoupdate.timer 2>/dev/null || systemctl start multica-autoupdate.timer 2>/dev/null || true; exit \$rc"

for _ in $(seq 1 30); do
  status="$(curl -s -o /dev/null -w '%{http_code}' -X POST "$public_url/api/webhooks/github" || true)"
  if [ "$status" = "401" ]; then
    echo "OK: $public_url/api/webhooks/github now verifies signatures (401 without one)."
    exit 0
  fi
  sleep 2
done
echo "Backend did not come up configured (last webhook status: ${status:-none})." >&2
exit 1
