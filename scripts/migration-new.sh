#!/usr/bin/env bash
#
# Allocate the next migration number and create the empty up/down pair.
#
#   make migration-new NAME=issue_status_icon
#
# Why this is a command instead of a runbook step: feature branches are cut
# from `kun` in parallel, so "max in my directory + 1" hands the same number to
# two branches, and golang-migrate applies both without complaining (it keys on
# the full filename, not the number). The number that decides is the one
# already on `origin/kun`, so this fetches it first and takes the maximum
# across both sides. `make migration-lint` (DENE-817) is the pre-merge gate for
# a fetch that went stale; this command only allocates.
#
# Rules, the collision gate and the renumbering procedure live in
# docs/kun/migration-numbering.md.

set -euo pipefail

REMOTE="${MIGRATION_NEW_REMOTE:-origin}"
BRANCH="${MIGRATION_NEW_BRANCH:-kun}"
MIGRATIONS_DIR="server/migrations"

fail() {
  printf 'migration-new: %s\n' "$1" >&2
  exit 1
}

bad_name() {
  printf 'migration-new: %s\n' "$1" >&2
  exit 2
}

usage() {
  cat >&2 <<'EOF'
usage: make migration-new NAME=<name>

Picks the next migration number from the newest migration on origin/kun and in
this checkout, then creates an empty, paired migration:

  server/migrations/<number>_<name>.up.sql
  server/migrations/<number>_<name>.down.sql

<name> is lowercase snake_case (letters, digits and single underscores), for
example:

  make migration-new NAME=issue_status_icon
EOF
}

name="${1-}"
if [ "$name" = "-h" ] || [ "$name" = "--help" ]; then
  usage
  exit 0
fi
if [ -z "$name" ]; then
  usage
  exit 2
fi
case "$name" in
  _* | *_ | *__* | *[!a-z0-9_]*)
    bad_name "NAME must be lowercase snake_case (letters, digits and single underscores), got '$name'"
    ;;
esac

# The number comes from git, never from the caller's idea of where the repo is.
root="$(git rev-parse --show-toplevel 2>/dev/null)" || fail "not inside a git work tree"
cd "$root"
[ -d "$MIGRATIONS_DIR" ] || fail "no $MIGRATIONS_DIR directory in $root"

# Allocation is only as good as the upstream ref it read. A failed fetch is a
# warning, not a stop: offline and air-gapped checkouts still need the command,
# and the pre-merge gate catches the stale number it produces. A ref that never
# existed is a hard stop, because then there is nothing to be stale against.
if fetch_error="$(git fetch --quiet "$REMOTE" "$BRANCH" 2>&1)"; then
  :
else
  printf 'migration-new: warning: git fetch %s %s failed; allocating from the last known %s/%s ref\n' \
    "$REMOTE" "$BRANCH" "$REMOTE" "$BRANCH" >&2
  if [ -n "$fetch_error" ]; then
    printf 'migration-new:   %s\n' "$fetch_error" >&2
  fi
fi
remote_ref="$REMOTE/$BRANCH"
if ! git rev-parse --verify --quiet "$remote_ref^{commit}" >/dev/null; then
  fail "cannot resolve $remote_ref; run 'git fetch $REMOTE $BRANCH' first"
fi

list_up_names() {
  local dir=$1 file
  for file in "$dir"/*.up.sql; do
    [ -f "$file" ] || continue
    printf '%s\n' "${file##*/}"
  done
}

# Reads "NNN_name.up.sql" lines and prints the largest numeric prefix, or 0 when
# there is none. 10# keeps a zero-padded prefix from being read as octal.
max_number() {
  local line prefix max=0
  while IFS= read -r line; do
    prefix="${line%%_*}"
    case "$prefix" in
      '' | *[!0-9]*) continue ;;
    esac
    if [ "$((10#$prefix))" -gt "$max" ]; then
      max=$((10#$prefix))
    fi
  done
  printf '%s\n' "$max"
}

if ! remote_tree="$(git ls-tree --name-only "$remote_ref:$MIGRATIONS_DIR" 2>&1)"; then
  fail "cannot list $MIGRATIONS_DIR in $remote_ref: $remote_tree"
fi

remote_max="$(printf '%s\n' "$remote_tree" | max_number)"
local_max="$(list_up_names "$MIGRATIONS_DIR" | max_number)"
if [ "$remote_max" -gt "$local_max" ]; then
  next=$((remote_max + 1))
else
  next=$((local_max + 1))
fi

stem="${next}_${name}"
up_rel="$MIGRATIONS_DIR/$stem.up.sql"
down_rel="$MIGRATIONS_DIR/$stem.down.sql"

if [ -e "$up_rel" ] || [ -e "$down_rel" ]; then
  fail "refusing to overwrite existing migration files for $stem"
fi

# A second change under a name already in use is legal but almost always a
# re-run of the command rather than a new migration, so say so.
for existing in "$MIGRATIONS_DIR"/*_"$name".up.sql; do
  [ -f "$existing" ] || continue
  printf 'migration-new: warning: a migration named %s already exists (%s)\n' "$name" "${existing##*/}" >&2
  break
done

: >"$up_rel"
: >"$down_rel"

printf 'migration-new: %s max %s, local max %s -> next %s\n' "$remote_ref" "$remote_max" "$local_max" "$next"
printf 'created %s\n' "$up_rel"
printf 'created %s\n' "$down_rel"
printf 'next: write the up/down SQL, then run make migration-lint before the PR\n'
