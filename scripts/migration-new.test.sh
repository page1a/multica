#!/usr/bin/env bash
#
# Fixtures for scripts/migration-new.sh, offline by construction.
#
# The command exists because the number has to come from `origin/kun` and not
# from the local directory: two branches allocating from their own tree pick
# the same number and golang-migrate applies both silently. That property is
# only observable against a remote that is ahead, so the fixtures below build
# real repos with a real (local-path) remote and assert on the allocated
# number, not on the text of the command. A failure here means an agent can
# collide a migration number again.

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="$ROOT_DIR/scripts/migration-new.sh"

fail() {
  printf 'migration-new.test: %s\n' "$1" >&2
  exit 1
}

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

UPSTREAM="$WORK/upstream"
CLONE="$WORK/clone"
FRESH="$WORK/fresh"

git_init() { # <dir> <branch>
  git init -q "$1"
  git -C "$1" symbolic-ref HEAD "refs/heads/$2"
  git -C "$1" config user.name "migration-new test"
  git -C "$1" config user.email "migration-new@test.local"
  git -C "$1" config commit.gpgsign false
}

add_migration() { # <repo> <number> <name>
  local dir="$1/server/migrations"
  mkdir -p "$dir"
  : >"$dir/${2}_${3}.up.sql"
  : >"$dir/${2}_${3}.down.sql"
}

commit_all() { # <repo> <message>
  git -C "$1" add -A
  git -C "$1" commit -q -m "$2"
}

# The clone is taken before the upstream gets 702, so the local tree stops at
# 701 while `origin/kun` moves to 702 on the next fetch. Allocating from the
# local directory would create a second 702.
git_init "$UPSTREAM" kun
add_migration "$UPSTREAM" 700 alpha
add_migration "$UPSTREAM" 701 beta
commit_all "$UPSTREAM" "seed migrations"

git clone -q "$UPSTREAM" "$CLONE"

add_migration "$UPSTREAM" 702 gamma
commit_all "$UPSTREAM" "upstream migration 702"

STATUS=0
STDOUT=""
STDERR=""
run_new() { # <repo> [args...]
  local repo=$1
  shift
  STATUS=0
  ( cd "$repo" && bash "$SCRIPT" "$@" ) >"$WORK/stdout" 2>"$WORK/stderr" || STATUS=$?
  STDOUT="$(cat "$WORK/stdout")"
  STDERR="$(cat "$WORK/stderr")"
}

expect_status() { # <expected> <label>
  [ "$STATUS" = "$1" ] || fail "$2: expected exit $1, got $STATUS
stdout:
$STDOUT
stderr:
$STDERR"
}

expect_contains() { # <haystack> <needle> <label>
  case "$1" in
    *"$2"*) ;;
    *) fail "$3: missing '$2' in:
$1" ;;
  esac
}

expect_empty() { # <path> <label>
  [ -f "$1" ] || fail "$2: $1 was not created"
  [ ! -s "$1" ] || fail "$2: $1 is not empty"
}

# 1. origin/kun decides. Local max is 701, upstream max is 702 -> 703, not 702.
run_new "$CLONE" first_change
expect_status 0 "origin ahead"
expect_contains "$STDOUT" "server/migrations/703_first_change.up.sql" "origin ahead: up file"
expect_contains "$STDOUT" "server/migrations/703_first_change.down.sql" "origin ahead: down file"
expect_empty "$CLONE/server/migrations/703_first_change.up.sql" "origin ahead"
expect_empty "$CLONE/server/migrations/703_first_change.down.sql" "origin ahead"
[ ! -e "$CLONE/server/migrations/702_first_change.up.sql" ] ||
  fail "origin ahead: allocated the upstream number 702"
# Allocation writes files; it must not stage or commit them for the caller.
[ "$(git -C "$CLONE" status --porcelain -- server/migrations/703_first_change.up.sql)" = \
  "?? server/migrations/703_first_change.up.sql" ] ||
  fail "origin ahead: the created migration is not a plain untracked file"

# 2. The files just created raise the local max, so the next allocation moves on.
run_new "$CLONE" second_change
expect_status 0 "second allocation"
expect_contains "$STDOUT" "server/migrations/704_second_change.up.sql" "second allocation"

# 3. A local number above origin/kun wins: local 900, upstream 702 -> 901.
add_migration "$CLONE" 900 local_only
commit_all "$CLONE" "local migration 900"
run_new "$CLONE" after_local
expect_status 0 "local ahead"
expect_contains "$STDOUT" "server/migrations/901_after_local.up.sql" "local ahead"

# 4. A failed fetch degrades to a warning and keeps working from the last known
#    ref; the pre-merge gate is the backstop for the stale number it yields.
git -C "$CLONE" remote set-url origin "$WORK/no-such-remote"
run_new "$CLONE" offline_change
expect_status 0 "offline fetch"
expect_contains "$STDERR" "warning: git fetch origin kun failed" "offline fetch: warning"
expect_contains "$STDOUT" "server/migrations/902_offline_change.up.sql" "offline fetch"

# 5. Re-using a name is allowed but called out.
run_new "$CLONE" after_local
expect_status 0 "duplicate name"
expect_contains "$STDERR" "a migration named after_local already exists (901_after_local.up.sql)" "duplicate name"

# 6. Bad input never creates a file.
run_new "$CLONE" Bad-Name
expect_status 2 "invalid name"
expect_contains "$STDERR" "lowercase snake_case" "invalid name"
run_new "$CLONE" ""
expect_status 2 "missing name"
expect_contains "$STDERR" "usage: make migration-new NAME=" "missing name"
[ -z "$(find "$CLONE/server/migrations" -name '*Bad-Name*' -print -quit)" ] ||
  fail "invalid name: a file was created anyway"

# 7. No origin/kun and no fetch -> hard stop, because nothing can be trusted.
git_init "$FRESH" kun
add_migration "$FRESH" 5 seed
commit_all "$FRESH" "seed migration"
run_new "$FRESH" no_remote
expect_status 1 "missing origin"
expect_contains "$STDERR" "cannot resolve origin/kun" "missing origin"
[ -z "$(find "$FRESH/server/migrations" -name '*no_remote*' -print -quit)" ] ||
  fail "missing origin: a file was created anyway"

echo "migration-new.test: ok"
