#!/usr/bin/env bash
# One-command upstream synchronization for the multica kun fork.
#
# Workflow:
#   1. Fetch upstream and origin remotes.
#   2. Check if upstream/main has new commits ahead of kun.
#   3. Fast-forward local main mirror to upstream/main and push to origin/main.
#   4. Create sync branch sync/upstream-<date> from kun.
#   5. Merge main into sync branch. If conflicts occur, stop, list conflicted files,
#      and abort the merge without naive resolution.
#   6. Check migration numbering with 'make migration-lint'.
#   7. Push sync branch to origin and open a PR targeting kun.
#
# Usage:
#   bash scripts/sync-upstream.sh [options]
#   bash scripts/sync-upstream.sh --dry-run
#
set -euo pipefail

UPSTREAM_REMOTE="${MULTICA_UPSTREAM_REMOTE:-upstream}"
ORIGIN_REMOTE="${MULTICA_ORIGIN_REMOTE:-origin}"
UPSTREAM_BRANCH="${MULTICA_UPSTREAM_BRANCH:-main}"
MIRROR_BRANCH="${MULTICA_MIRROR_BRANCH:-main}"
TARGET_BRANCH="${MULTICA_TARGET_BRANCH:-kun}"
CUSTOM_DATE=""
CUSTOM_BRANCH=""
DRY_RUN=0
SKIP_FETCH=0
SKIP_MIGRATION_LINT=0

if [ -t 1 ] || [ -t 2 ]; then
  BOLD='\033[1m'
  GREEN='\033[0;32m'
  YELLOW='\033[0;33m'
  RED='\033[0;31m'
  CYAN='\033[0;36m'
  RESET='\033[0m'
else
  BOLD='' GREEN='' YELLOW='' RED='' CYAN='' RESET=''
fi

info() { printf "${BOLD}${CYAN}==> %s${RESET}\n" "$*"; }
ok()   { printf "${BOLD}${GREEN}✓ %s${RESET}\n" "$*"; }
warn() { printf "${BOLD}${YELLOW}⚠ %s${RESET}\n" "$*" >&2; }
fail() { printf "${BOLD}${RED}✗ %s${RESET}\n" "$*" >&2; exit 1; }

usage() {
  cat <<'EOF'
Usage: bash scripts/sync-upstream.sh [options]

Sync upstream repository changes into the kun fork branch.

Workflow:
  1. Fetch upstream and origin remotes.
  2. Check if upstream/main has new commits ahead of kun.
  3. Fast-forward local main mirror to upstream/main and push to origin/main.
  4. Create sync branch sync/upstream-<date> from kun.
  5. Merge main into sync branch. If conflicts occur, stop, list conflicted files,
     and abort the merge without attempting naive auto-resolution.
  6. Run 'make migration-lint' to verify migration numbering.
  7. Push sync branch to origin and open a PR targeting kun.

Options:
  --dry-run                 Simulate the workflow without pushing to remotes or opening PR
  --skip-fetch              Skip fetching remotes (useful when already fetched or offline)
  --skip-migration-lint     Skip running 'make migration-lint'
  --skip-checks             Alias for --skip-migration-lint
  --upstream-remote REMOTE  Upstream remote name (default: upstream)
  --origin-remote REMOTE    Fork remote name (default: origin)
  --upstream-branch BRANCH  Upstream branch name (default: main)
  --mirror-branch BRANCH    Mirror branch name (default: main)
  --target-branch BRANCH    Base branch to sync into (default: kun)
  --date YYYYMMDD           Date string used in branch name (default: today's date)
  --branch-name NAME        Explicit branch name (default: sync/upstream-<date>)
  -h, --help                Show this help message

Environment:
  MULTICA_UPSTREAM_REMOTE   Default for --upstream-remote (upstream)
  MULTICA_ORIGIN_REMOTE     Default for --origin-remote (origin)
  MULTICA_UPSTREAM_BRANCH   Default for --upstream-branch (main)
  MULTICA_MIRROR_BRANCH     Default for --mirror-branch (main)
  MULTICA_TARGET_BRANCH     Default for --target-branch (kun)
EOF
}

parse_conflict_files() {
  awk '
    /^CONFLICT \(content\): Merge conflict in / { sub(/^CONFLICT \(content\): Merge conflict in /, ""); print; next }
    /^CONFLICT \(add\/add\): Merge conflict in / { sub(/^CONFLICT \(add\/add\): Merge conflict in /, ""); print; next }
    /^CONFLICT \(submodule\): Merge conflict in / { sub(/^CONFLICT \(submodule\): Merge conflict in /, ""); print; next }
    /^CONFLICT \(file\/directory\): Merge conflict in / { sub(/^CONFLICT \(file\/directory\): Merge conflict in /, ""); print; next }
    /^CONFLICT \(distinct types\): Merge conflict in / { sub(/^CONFLICT \(distinct types\): Merge conflict in /, ""); print; next }
    /^CONFLICT \(modify\/delete\): / {
      sub(/^CONFLICT \(modify\/delete\): /, "");
      sub(/ deleted in .*/, "");
      print;
      next
    }
    /^CONFLICT \(delete\/modify\): / {
      sub(/^CONFLICT \(delete\/modify\): /, "");
      sub(/ deleted in .*/, "");
      print;
      next
    }
    /^CONFLICT / {
      print $0;
    }
  ' | sort -u
}

while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run)
      DRY_RUN=1
      shift
      ;;
    --skip-fetch)
      SKIP_FETCH=1
      shift
      ;;
    --skip-migration-lint|--skip-checks)
      SKIP_MIGRATION_LINT=1
      shift
      ;;
    --upstream-remote)
      [ $# -ge 2 ] || fail "Missing value for --upstream-remote"
      UPSTREAM_REMOTE="$2"
      shift 2
      ;;
    --origin-remote)
      [ $# -ge 2 ] || fail "Missing value for --origin-remote"
      ORIGIN_REMOTE="$2"
      shift 2
      ;;
    --upstream-branch)
      [ $# -ge 2 ] || fail "Missing value for --upstream-branch"
      UPSTREAM_BRANCH="$2"
      shift 2
      ;;
    --mirror-branch)
      [ $# -ge 2 ] || fail "Missing value for --mirror-branch"
      MIRROR_BRANCH="$2"
      shift 2
      ;;
    --target-branch)
      [ $# -ge 2 ] || fail "Missing value for --target-branch"
      TARGET_BRANCH="$2"
      shift 2
      ;;
    --date)
      [ $# -ge 2 ] || fail "Missing value for --date"
      CUSTOM_DATE="$2"
      shift 2
      ;;
    --branch-name)
      [ $# -ge 2 ] || fail "Missing value for --branch-name"
      CUSTOM_BRANCH="$2"
      shift 2
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      fail "Unknown argument: $1. Run with --help for usage."
      ;;
  esac
done

SYNC_DATE="${CUSTOM_DATE:-$(date +%Y%m%d)}"

# 1. Check git repository
if ! git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
  fail "Not inside a git repository."
fi

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "$REPO_ROOT"

ORIG_BRANCH="$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "")"

# 2. Check remotes
if ! git remote get-url "$UPSTREAM_REMOTE" >/dev/null 2>&1; then
  fail "Remote '$UPSTREAM_REMOTE' not found.
Add it with:
  git remote add $UPSTREAM_REMOTE https://github.com/multica-ai/multica.git"
fi

if ! git remote get-url "$ORIGIN_REMOTE" >/dev/null 2>&1; then
  fail "Remote '$ORIGIN_REMOTE' not found."
fi

upstream_url="$(git remote get-url "$UPSTREAM_REMOTE" 2>/dev/null || true)"
case "$upstream_url" in
  *multica-io/multica*)
    warn "Remote '$UPSTREAM_REMOTE' points to 'multica-io/multica'; official upstream repo is 'multica-ai/multica'."
    ;;
esac

# 3. Clean working tree check
if [ "$DRY_RUN" -eq 0 ]; then
  if [ -n "$(git status --porcelain)" ]; then
    fail "Working tree is dirty. Please stash or commit your changes before syncing."
  fi
fi

# 4. Fetch remotes
if [ "$SKIP_FETCH" -eq 0 ]; then
  info "Fetching remotes: '$UPSTREAM_REMOTE' and '$ORIGIN_REMOTE'..."
  git fetch "$UPSTREAM_REMOTE" --prune
  git fetch "$ORIGIN_REMOTE" --prune
  ok "Remotes fetched."
fi

# 5. Check if upstream has new commits ahead of target branch
info "Comparing '$TARGET_BRANCH' against '$UPSTREAM_REMOTE/$UPSTREAM_BRANCH'..."
if ! git rev-parse --verify "$UPSTREAM_REMOTE/$UPSTREAM_BRANCH" >/dev/null 2>&1; then
  fail "Ref '$UPSTREAM_REMOTE/$UPSTREAM_BRANCH' not found. Run git fetch $UPSTREAM_REMOTE."
fi
if ! git rev-parse --verify "$TARGET_BRANCH" >/dev/null 2>&1; then
  fail "Ref '$TARGET_BRANCH' not found."
fi

commits="$(git log --oneline "${TARGET_BRANCH}..${UPSTREAM_REMOTE}/${UPSTREAM_BRANCH}")"
if [ -z "$commits" ]; then
  ok "Upstream has no new commits compared to '$TARGET_BRANCH'. Nothing to sync."
  exit 0
fi

commit_count="$(printf "%s\n" "$commits" | grep -c . || echo 0)"
info "Found $commit_count new commit(s) on '$UPSTREAM_REMOTE/$UPSTREAM_BRANCH' ahead of '$TARGET_BRANCH':"
printf "%s\n" "$commits" | head -n 10
if [ "$commit_count" -gt 10 ]; then
  printf "  ... and %d more commit(s)\n" "$((commit_count - 10))"
fi

# 6. Verify main mirror fast-forwardability
if git show-ref --verify --quiet "refs/heads/$MIRROR_BRANCH"; then
  if ! git merge-base --is-ancestor "$MIRROR_BRANCH" "$UPSTREAM_REMOTE/$UPSTREAM_BRANCH"; then
    fail "Mirror branch '$MIRROR_BRANCH' has diverged from '$UPSTREAM_REMOTE/$UPSTREAM_BRANCH' and cannot be fast-forwarded. '$MIRROR_BRANCH' must strictly mirror upstream."
  fi
elif git show-ref --verify --quiet "refs/remotes/$ORIGIN_REMOTE/$MIRROR_BRANCH"; then
  if ! git merge-base --is-ancestor "$ORIGIN_REMOTE/$MIRROR_BRANCH" "$UPSTREAM_REMOTE/$UPSTREAM_BRANCH"; then
    fail "Remote mirror branch '$ORIGIN_REMOTE/$MIRROR_BRANCH' has diverged from '$UPSTREAM_REMOTE/$UPSTREAM_BRANCH' and cannot be fast-forwarded."
  fi
fi

# 7. Determine sync branch name
if [ -n "$CUSTOM_BRANCH" ]; then
  SYNC_BRANCH="$CUSTOM_BRANCH"
else
  SYNC_BRANCH="sync/upstream-${SYNC_DATE}"
  suffix=1
  base_sync_branch="$SYNC_BRANCH"
  while git show-ref --verify --quiet "refs/heads/$SYNC_BRANCH"; do
    SYNC_BRANCH="${base_sync_branch}.${suffix}"
    suffix=$((suffix + 1))
  done
fi

# 8. Dry-run simulation
if [ "$DRY_RUN" -eq 1 ]; then
  info "[dry-run] Mirror branch '$MIRROR_BRANCH' can be fast-forwarded to '$UPSTREAM_REMOTE/$UPSTREAM_BRANCH'."
  info "[dry-run] Would run: git push $ORIGIN_REMOTE $MIRROR_BRANCH"
  info "[dry-run] Simulating 3-way merge between '$TARGET_BRANCH' and '$UPSTREAM_REMOTE/$UPSTREAM_BRANCH'..."

  set +e
  merge_tree_output="$(git merge-tree --write-tree "$TARGET_BRANCH" "$UPSTREAM_REMOTE/$UPSTREAM_BRANCH" 2>&1)"
  merge_tree_status=$?
  set -e

  if [ "$merge_tree_status" -ne 0 ]; then
    conflict_files="$(printf "%s\n" "$merge_tree_output" | parse_conflict_files)"
    conflict_count="$(printf "%s\n" "$conflict_files" | grep -c . || echo 0)"
    warn "==> [dry-run] Pre-merge check: CONFLICTS DETECTED ($conflict_count file(s)):"
    while IFS= read -r f; do
      [ -n "$f" ] && printf "  ! %s\n" "$f" >&2
    done <<< "$conflict_files"
    warn "==> [dry-run] Automatic sync would stop here. Conflicts must be resolved manually."
    warn "==> [dry-run] Target sync branch would be: $SYNC_BRANCH"
    exit 1
  fi

  ok "[dry-run] Pre-merge check: CLEAN (no conflicts)."
  ok "[dry-run] Would create branch: '$SYNC_BRANCH' from '$TARGET_BRANCH'"
  ok "[dry-run] Would merge: '$MIRROR_BRANCH'"
  if [ "$SKIP_MIGRATION_LINT" -eq 0 ]; then
    ok "[dry-run] Would run: make migration-lint"
  fi
  ok "[dry-run] Would push: '$ORIGIN_REMOTE $SYNC_BRANCH'"
  ok "[dry-run] Would create PR targeting '$TARGET_BRANCH' via 'gh pr create'"
  exit 0
fi

# 9. Real run: Update mirror branch
info "Updating mirror branch '$MIRROR_BRANCH' to '$UPSTREAM_REMOTE/$UPSTREAM_BRANCH'..."
if [ "$ORIG_BRANCH" = "$MIRROR_BRANCH" ]; then
  git merge --ff-only "$UPSTREAM_REMOTE/$UPSTREAM_BRANCH"
elif git show-ref --verify --quiet "refs/heads/$MIRROR_BRANCH"; then
  git fetch . "$UPSTREAM_REMOTE/$UPSTREAM_BRANCH:$MIRROR_BRANCH"
else
  git branch "$MIRROR_BRANCH" "$UPSTREAM_REMOTE/$UPSTREAM_BRANCH"
fi

info "Pushing mirror branch '$MIRROR_BRANCH' to '$ORIGIN_REMOTE'..."
git push "$ORIGIN_REMOTE" "$MIRROR_BRANCH"
ok "Mirror branch '$MIRROR_BRANCH' updated and pushed."

# 10. Create sync branch
info "Creating sync branch '$SYNC_BRANCH' from '$TARGET_BRANCH'..."
git checkout -b "$SYNC_BRANCH" "$TARGET_BRANCH"

# 11. Merge mirror branch
info "Merging '$MIRROR_BRANCH' into '$SYNC_BRANCH'..."
set +e
merge_output="$(git merge --no-ff "$MIRROR_BRANCH" -m "sync: merge upstream $UPSTREAM_BRANCH ($SYNC_DATE) into $TARGET_BRANCH" 2>&1)"
merge_status=$?
set -e

if [ "$merge_status" -ne 0 ]; then
  conflict_files="$(git diff --name-only --diff-filter=U 2>/dev/null || true)"
  if [ -z "$conflict_files" ]; then
    conflict_files="$(git status --porcelain | grep -E '^(U.|.U|AA|DD)' | awk '{print $2}' || true)"
  fi
  conflict_count="$(printf "%s\n" "$conflict_files" | grep -c . || echo 0)"

  warn "Merge failed with conflicts ($conflict_count file(s))!"
  while IFS= read -r f; do
    [ -n "$f" ] && printf "  ! %s\n" "$f" >&2
  done <<< "$conflict_files"

  info "Aborting merge to restore working tree..."
  git merge --abort
  if [ -n "$ORIG_BRANCH" ] && [ "$ORIG_BRANCH" != "$SYNC_BRANCH" ]; then
    git checkout "$ORIG_BRANCH"
  fi

  fail "Conflicts detected. Merge aborted. Please resolve conflicts manually on '$SYNC_BRANCH'."
fi

ok "Merge succeeded without conflicts."

# 12. Run migration lint
if [ "$SKIP_MIGRATION_LINT" -eq 0 ]; then
  info "Running migration lint ('make migration-lint')..."
  if ! make migration-lint; then
    warn "make migration-lint failed! Duplicate migration prefix detected."
    warn "Please check and fix migration numbering on branch '$SYNC_BRANCH'."
    fail "Migration lint failed."
  fi
  ok "Migration lint passed."
fi

# 13. Push sync branch
info "Pushing '$SYNC_BRANCH' to '$ORIGIN_REMOTE'..."
git push -u "$ORIGIN_REMOTE" "$SYNC_BRANCH"
ok "Branch '$SYNC_BRANCH' pushed to '$ORIGIN_REMOTE'."

# 14. Create PR targeting kun
if command -v gh >/dev/null 2>&1; then
  info "Creating PR targeting '$TARGET_BRANCH'..."
  pr_title="sync: upstream main ${SYNC_DATE}"
  pr_body="Sync upstream \`${UPSTREAM_REMOTE}/${UPSTREAM_BRANCH}\` into \`${TARGET_BRANCH}\`.

Commits:
\`\`\`
$(printf "%s\n" "$commits" | head -n 30)
\`\`\`"
  gh pr create --base "$TARGET_BRANCH" --head "$SYNC_BRANCH" --title "$pr_title" --body "$pr_body"
  ok "PR created successfully."
else
  warn "'gh' CLI not found. Branch '$SYNC_BRANCH' was pushed to '$ORIGIN_REMOTE'."
  warn "Please create the PR manually targeting '$TARGET_BRANCH'."
fi

ok "Upstream sync completed successfully!"
