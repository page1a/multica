#!/usr/bin/env bash
# Unit and integration test suite for scripts/sync-upstream.sh.
# Uses isolated temporary Git repositories without network or GitHub dependencies.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SYNC_SCRIPT="$ROOT_DIR/scripts/sync-upstream.sh"

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

fail() {
  echo "FAIL: $*" >&2
  exit 1
}

require_contains() {
  local file=$1 expected=$2 what=$3
  if ! grep -Fq -- "$expected" "$file"; then
    echo "Observed $file:" >&2
    sed 's/^/  /' "$file" >&2 || true
    fail "$what: expected to find '$expected' in $file"
  fi
}

require_not_contains() {
  local file=$1 unexpected=$2 what=$3
  if grep -Fq -- "$unexpected" "$file"; then
    echo "Observed $file:" >&2
    sed 's/^/  /' "$file" >&2 || true
    fail "$what: did not expect '$unexpected' in $file"
  fi
}

echo "==> Test 1: --help output"
help_out="$work/help.txt"
bash "$SYNC_SCRIPT" --help >"$help_out" 2>&1
require_contains "$help_out" "Usage: bash scripts/sync-upstream.sh" "help output"
require_contains "$help_out" "--dry-run" "help options"

echo "==> Test 2: Missing upstream remote fails cleanly"
fake_repo="$work/fake_repo"
mkdir -p "$fake_repo"
git -C "$fake_repo" init -q
git -C "$fake_repo" config user.email test@example.com
git -C "$fake_repo" config user.name "Test User"
touch "$fake_repo/file.txt"
git -C "$fake_repo" add .
git -C "$fake_repo" commit -qm "init"

missing_remote_out="$work/missing_remote.txt"
set +e
(cd "$fake_repo" && bash "$SYNC_SCRIPT" --dry-run >"$missing_remote_out" 2>&1)
code=$?
set -e
[ "$code" -ne 0 ] || fail "Expected missing remote to fail"
require_contains "$missing_remote_out" "Remote 'upstream' not found" "missing remote check"

# ---------------------------------------------------------------------------
# Set up a real origin (fork) and real upstream bare repos with a clone
# ---------------------------------------------------------------------------
echo "==> Setting up mock upstream, origin, and local clone..."
upstream_bare="$work/upstream.git"
origin_bare="$work/origin.git"
clone="$work/clone"

git init -q --bare "$upstream_bare"
git init -q --bare "$origin_bare"

# Seed base commit on upstream main
seed="$work/seed"
git init -q "$seed"
git -C "$seed" config user.email test@example.com
git -C "$seed" config user.name "Seed User"
mkdir -p "$seed/scripts"
cat >"$seed/scripts/sync-upstream.sh" <"$SYNC_SCRIPT"
chmod +x "$seed/scripts/sync-upstream.sh"
cat >"$seed/Makefile" <<'MAKEFILE'
.PHONY: migration-lint
migration-lint:
	@echo "Migration lint passed."
MAKEFILE
cat >"$seed/common.txt" <<'EOF'
line 1
line 2
line 3
EOF
git -C "$seed" add -A
git -C "$seed" commit -qm "initial commit"
git -C "$seed" branch -M main

git -C "$seed" remote add upstream "$upstream_bare"
git -C "$seed" push -q upstream main

git -C "$seed" remote add origin "$origin_bare"
git -C "$seed" push -q origin main

# Fork branches out kun on origin
git -C "$seed" checkout -qb kun
echo "kun feature 1" >> "$seed/kun_file.txt"
git -C "$seed" add -A
git -C "$seed" commit -qm "kun fork feature"
git -C "$seed" push -q origin kun

# Clone local repo from origin
git clone -q -b kun "$origin_bare" "$clone"
git -C "$clone" config user.email test@example.com
git -C "$clone" config user.name "Clone User"
git -C "$clone" remote add upstream "$upstream_bare"
git -C "$clone" fetch -q upstream

echo "==> Test 3: Upstream has no new commits (up to date)"
up_to_date_out="$work/up_to_date.txt"
(cd "$clone" && bash "$SYNC_SCRIPT" --dry-run --skip-fetch >"$up_to_date_out" 2>&1)
require_contains "$up_to_date_out" "Upstream has no new commits" "up to date check"

# ---------------------------------------------------------------------------
# Now add a commit on upstream/main that cleanly merges with kun
# ---------------------------------------------------------------------------
echo "==> Adding clean commit to upstream/main..."
clean_seed="$work/clean_seed"
git clone -q -b main "$upstream_bare" "$clean_seed"
git -C "$clean_seed" config user.email test@example.com
git -C "$clean_seed" config user.name "Upstream Dev"
echo "upstream new feature" > "$clean_seed/upstream_feature.txt"
git -C "$clean_seed" add -A
git -C "$clean_seed" commit -qm "feat: upstream new feature"
git -C "$clean_seed" push -q origin main

git -C "$clone" fetch -q upstream

echo "==> Test 4: Upstream clean merge in --dry-run mode"
clean_dry_run_out="$work/clean_dry_run.txt"
(cd "$clone" && bash "$SYNC_SCRIPT" --dry-run --skip-fetch --date 20260924 >"$clean_dry_run_out" 2>&1)
require_contains "$clean_dry_run_out" "Found 1 new commit(s)" "commit count check"
require_contains "$clean_dry_run_out" "CLEAN (no conflicts)" "clean check"
require_contains "$clean_dry_run_out" "Would create branch: 'sync/upstream-20260924'" "branch name check"
require_contains "$clean_dry_run_out" "Would run: make migration-lint" "migration-lint check"

echo "==> Test 5: Upstream clean merge in real run"
# Stub gh command in PATH for PR creation
stub_bin="$work/bin"
mkdir -p "$stub_bin"
gh_log="$work/gh_log.txt"
cat >"$stub_bin/gh" <<EOF
#!/usr/bin/env bash
printf 'gh %s\n' "\$*" >>"$gh_log"
echo "https://github.com/jeff-kunkun/multica/pull/999"
exit 0
EOF
chmod +x "$stub_bin/gh"

clean_real_out="$work/clean_real.txt"
(
  export PATH="$stub_bin:$PATH"
  cd "$clone" && bash "$SYNC_SCRIPT" --skip-fetch --date 20260924 >"$clean_real_out" 2>&1
)
require_contains "$clean_real_out" "Mirror branch 'main' updated and pushed." "mirror update check"
require_contains "$clean_real_out" "Merge succeeded without conflicts." "merge success check"
require_contains "$clean_real_out" "Migration lint passed." "migration lint execution check"
require_contains "$clean_real_out" "Branch 'sync/upstream-20260924' pushed" "push check"
require_contains "$clean_real_out" "PR created successfully." "pr created check"
require_contains "$gh_log" "pr create --base kun --head sync/upstream-20260924" "gh invocation"

# ---------------------------------------------------------------------------
# Now add a conflicting commit to upstream/main
# ---------------------------------------------------------------------------
echo "==> Adding conflicting commit to upstream/main and kun..."
# Modify common.txt differently on kun and upstream
echo "kun edit" > "$clone/common.txt"
git -C "$clone" checkout -q kun
git -C "$clone" add common.txt
git -C "$clone" commit -qm "kun modified common.txt"
git -C "$clone" push -q origin kun

echo "upstream edit" > "$clean_seed/common.txt"
git -C "$clean_seed" add common.txt
git -C "$clean_seed" commit -qm "upstream modified common.txt"
git -C "$clean_seed" push -q origin main

git -C "$clone" fetch -q upstream

echo "==> Test 6: Conflict detected in --dry-run mode"
conflict_dry_run_out="$work/conflict_dry_run.txt"
set +e
(cd "$clone" && bash "$SYNC_SCRIPT" --dry-run --skip-fetch --date 20260925 >"$conflict_dry_run_out" 2>&1)
conflict_code=$?
set -e
[ "$conflict_code" -ne 0 ] || fail "Expected dry-run with conflicts to exit non-zero"
require_contains "$conflict_dry_run_out" "CONFLICTS DETECTED" "dry-run conflict detection"
require_contains "$conflict_dry_run_out" "common.txt" "dry-run conflicted file reported"
require_contains "$conflict_dry_run_out" "Automatic sync would stop here" "dry-run stop message"

echo "==> Test 7: Conflict detected in real run aborts merge cleanly"
conflict_real_out="$work/conflict_real.txt"
set +e
(
  export PATH="$stub_bin:$PATH"
  cd "$clone" && bash "$SYNC_SCRIPT" --skip-fetch --date 20260925 >"$conflict_real_out" 2>&1
)
real_code=$?
set -e
[ "$real_code" -ne 0 ] || fail "Expected real run with conflicts to exit non-zero"
require_contains "$conflict_real_out" "Merge failed with conflicts" "real conflict message"
require_contains "$conflict_real_out" "common.txt" "real conflicted file reported"
require_contains "$conflict_real_out" "Aborting merge to restore working tree" "real abort message"

# Verify working tree is clean after abort
clone_status="$(git -C "$clone" status --porcelain)"
[ -z "$clone_status" ] || fail "Expected clone working tree to be clean after aborted merge, got: $clone_status"

echo "==> Test 8: Dirty working tree is rejected before real run"
touch "$clone/dirty.txt"
dirty_out="$work/dirty.txt"
set +e
(cd "$clone" && bash "$SYNC_SCRIPT" --skip-fetch >"$dirty_out" 2>&1)
dirty_code=$?
set -e
[ "$dirty_code" -ne 0 ] || fail "Expected dirty working tree to fail"
require_contains "$dirty_out" "Working tree is dirty" "dirty tree check"
rm "$clone/dirty.txt"

echo "✓ All sync-upstream tests passed!"
