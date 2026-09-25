#!/usr/bin/env bash
set -euo pipefail

usage() {
  cat <<'USAGE'
Usage: scripts/desktop-release.sh cut --channel test [--dry-run]
       scripts/desktop-release.sh cut --channel stable --confirm-stable [--dry-run]

Cuts and pushes the next desktop release tag from the kun branch.
USAGE
}

fail() { echo "error: $*" >&2; exit 1; }

command_name=${1:-}
[[ "$command_name" == "cut" ]] || { usage >&2; exit 2; }
shift

channel=""
dry_run=false
confirm_stable=false
while (($#)); do
  case "$1" in
    --channel)
      (($# >= 2)) || fail "--channel requires test or stable"
      channel=$2; shift 2 ;;
    --dry-run) dry_run=true; shift ;;
    --confirm-stable) confirm_stable=true; shift ;;
    -h|--help) usage; exit 0 ;;
    *) fail "unknown option: $1" ;;
  esac
done

[[ "$channel" == test || "$channel" == stable ]] || fail "--channel must be test or stable"
if [[ "$channel" == stable && "$confirm_stable" != true ]]; then
  fail "refusing --channel stable; add --confirm-stable explicitly"
fi

repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"
branch=$(git branch --show-current)
head_sha=$(git rev-parse HEAD)
remote_sha=$(git rev-parse origin/kun 2>/dev/null || true)

if [[ "$dry_run" != true ]]; then
  [[ "$branch" == kun ]] || fail "must run on kun (current branch: $branch)"
  [[ -z "$(git status --porcelain)" ]] || fail "worktree must be clean"
  git fetch origin --prune
  remote_sha=$(git rev-parse origin/kun)
fi

latest_stable=$(git tag -l 'v[0-9]*.[0-9]*.[0-9]*' | awk '/^v[0-9]+\.[0-9]+\.[0-9]+$/ {print}' | sort -V | tail -n1)
if [[ -z "$latest_stable" ]]; then
  base="v0.1.0"
else
  if [[ "$latest_stable" =~ ^v([0-9]+)\.([0-9]+)\.([0-9]+)$ ]]; then
    base="v${BASH_REMATCH[1]}.${BASH_REMATCH[2]}.$((BASH_REMATCH[3] + 1))"
  else
    fail "could not parse latest stable tag: $latest_stable"
  fi
fi

if [[ "$channel" == test ]]; then
  next_n=$(git tag -l "${base}-test.*" | sed -nE "s/^${base//./\\.}-test\.([0-9]+)$/\1/p" | sort -n | tail -n1)
  next_n=${next_n:-0}
  tag="${base}-test.$((next_n + 1))"
else
  tag="$base"
fi

if [[ "$dry_run" == true ]]; then
  echo "desktop release dry-run"
  echo "  channel: $channel"
  echo "  branch: $branch"
  echo "  commit: $head_sha"
  echo "  latest stable: ${latest_stable:-none}"
  echo "  next tag: $tag"
  echo "  checks:"
  [[ "$branch" == kun ]] && echo "    PASS branch is kun" || echo "    FAIL branch is not kun"
  [[ -z "$(git status --porcelain)" ]] && echo "    PASS worktree is clean" || echo "    FAIL worktree is dirty"
  if [[ -n "$remote_sha" && "$head_sha" == "$remote_sha" ]]; then
    echo "    PASS HEAD matches origin/kun"
  elif [[ -n "$remote_sha" ]]; then
    echo "    FAIL HEAD does not match origin/kun ($remote_sha)"
  else
    echo "    WARN origin/kun is unavailable (fetch would be required)"
  fi
  if [[ "$channel" == stable ]]; then
    echo "  confirmation: --confirm-stable supplied"
  fi
  echo "  would run: git fetch origin --prune"
  echo "  would run: git tag $tag"
  echo "  would run: git push origin $tag"
  exit 0
fi

[[ "$head_sha" == "$remote_sha" ]] || fail "HEAD is not synchronized with origin/kun; update kun and retry"

# A stable tag on this commit would make package.mjs choose the wrong channel.
if git tag --points-at HEAD | grep -Eq '^v[0-9]+\.[0-9]+\.[0-9]+$'; then
  fail "a stable tag already points at this commit; advance kun before cutting a test release"
fi

if git rev-parse "$tag" >/dev/null 2>&1; then
  fail "tag already exists: $tag"
fi

echo "cutting $tag from $head_sha"
git tag "$tag"
git push origin "$tag"
echo "pushed $tag; GitHub Actions will build the desktop release"
