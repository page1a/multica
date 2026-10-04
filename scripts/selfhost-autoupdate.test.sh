#!/usr/bin/env bash
# Stubbed coverage for scripts/selfhost-autoupdate.sh, plus the idempotency the
# installer promises.
#
# No Docker daemon, no network, no systemd: docker and curl are stubs, but git
# is real and the checkout under test is a real clone of a local bare repo, so
# "rollback puts the source back" is asserted on real refs rather than on a log
# line. The two answers that decide everything — /readyz and /health — are files
# the docker stub rewrites when it recreates a container, which is exactly what
# a real upgrade does.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SCRIPT="$ROOT_DIR/scripts/selfhost-autoupdate.sh"
INSTALLER="$ROOT_DIR/scripts/install-selfhost-autoupdate.sh"
WAIT_SCRIPT="$ROOT_DIR/scripts/selfhost-wait.sh"

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

require_eq() {
  local actual=$1 expected=$2 what=$3
  [ "$actual" = "$expected" ] || fail "$what: expected '$expected', got '$actual'"
}

# ---------------------------------------------------------------------------
# A real origin, a real clone, and two commits so "the box is one behind" is a
# fact about refs instead of a mocked string.
# ---------------------------------------------------------------------------
origin="$work/origin.git"
seed="$work/seed"
checkout="$work/checkout"

git init -q --bare "$origin"
git init -q "$seed"
git -C "$seed" config user.email test@example.com
git -C "$seed" config user.name "Autoupdate Test"
mkdir -p "$seed/scripts" "$seed/deploy/systemd"
cp "$WAIT_SCRIPT" "$SCRIPT" "$INSTALLER" "$seed/scripts/"
cp "$ROOT_DIR/docker-compose.selfhost.yml" "$ROOT_DIR/docker-compose.selfhost.build.yml" "$seed/"
cp "$ROOT_DIR"/deploy/systemd/multica-autoupdate.service "$ROOT_DIR"/deploy/systemd/multica-autoupdate.timer "$seed/deploy/systemd/"
git -C "$seed" add -A
git -C "$seed" commit -qm "A: what the box is running"
git -C "$seed" branch -M kun
git -C "$seed" remote add origin "$origin"
git -C "$seed" push -q origin kun
sha_a="$(git -C "$seed" rev-parse HEAD)"

printf 'B\n' >"$seed/CHANGELOG.md"
git -C "$seed" add -A
git -C "$seed" commit -qm "B: the new upstream tip"
git -C "$seed" push -q origin kun
sha_b="$(git -C "$seed" rev-parse HEAD)"

git clone -q -b kun "$origin" "$checkout"

# ---------------------------------------------------------------------------
# Stubs
# ---------------------------------------------------------------------------
stub_bin="$work/bin"
stub_state="$work/stubstate"
docker_log="$work/docker.log"
state_dir="$work/state"
mkdir -p "$stub_bin" "$stub_state" "$state_dir"

cat >"$stub_bin/docker" <<'STUB'
#!/usr/bin/env bash
set -u
args="$*"
printf 'docker %s\n' "$args" >>"${STUB_LOG:?}"
case " $args " in
*" build "*)
  printf 'build VERSION=%s COMMIT=%s DATE=%s\n' "${VERSION:-}" "${COMMIT:-}" "${DATE:-}" >>"${STUB_LOG:?}"
  if [ "${STUB_BUILD_FAILS:-0}" = "1" ]; then
    echo "stub docker: build failed" >&2
    exit 1
  fi
  exit 0
  ;;
esac
case " $args " in
*" port backend 8080 "*)
  printf '0.0.0.0:%s\n' "${STUB_BACKEND_PORT:-8080}"
  exit 0
  ;;
*" port frontend 3000 "*)
  printf '0.0.0.0:%s\n' "${STUB_FRONTEND_PORT:-3000}"
  exit 0
  ;;
esac
case " $args " in
*" pull "*)
  if [ "${STUB_PULL_FAILS:-0}" = "1" ]; then
    echo "stub docker: manifest unknown" >&2
    exit 1
  fi
  exit 0
  ;;
esac
case " $args " in
*" image inspect "*)
  [ "${STUB_IMAGES_EXIST:-1}" = "1" ] || exit 1
  exit 0
  ;;
esac
case " $args " in
*" tag "*)
  if [ "${STUB_TAG_FAILS:-0}" = "1" ]; then
    echo "stub docker: tag failed" >&2
    exit 1
  fi
  exit 0
  ;;
esac
case " $args " in
*" up "*)
  if [ "${STUB_UP_FAILS:-0}" = "1" ]; then
    echo "stub docker: up failed" >&2
    exit 1
  fi
  case " $args " in
  *" --force-recreate "*)
    # A recreate is what actually swaps the running commit. The rollback is
    # distinguishable because the script labels it with the *old* commit: a
    # rollback rebuild that lied about its commit would defeat the whole check.
    if [ -n "${STUB_OLD_COMMIT:-}" ] && [ "${COMMIT:-}" = "${STUB_OLD_COMMIT}" ]; then
      printf '%s' "${STUB_ROLLBACK_READY:-1}" >"${STUB_STATE_DIR:?}/ready"
      printf '%s' "${STUB_ROLLBACK_COMMIT:-$STUB_OLD_COMMIT}" >"${STUB_STATE_DIR:?}/health_commit"
      printf 'recreate-rollback\n' >>"${STUB_LOG:?}"
    else
      printf '%s' "${STUB_READY_ON_UPDATE:-1}" >"${STUB_STATE_DIR:?}/ready"
      printf '%s' "${STUB_UPDATE_COMMIT_VALUE:-${COMMIT:-}}" >"${STUB_STATE_DIR:?}/health_commit"
      printf 'recreate-update\n' >>"${STUB_LOG:?}"
    fi
    ;;
  esac
  exit 0
  ;;
esac
exit 0
STUB

cat >"$stub_bin/curl" <<'STUB'
#!/usr/bin/env bash
set -u
printf 'curl %s\n' "$*" >>"${STUB_LOG:?}"
url=""
for arg in "$@"; do
  case "$arg" in
  http*) url="$arg" ;;
  esac
done
case "$url" in
*/readyz)
  if [ "$(cat "${STUB_STATE_DIR:?}/ready" 2>/dev/null || echo 0)" = "1" ]; then
    printf '{"status":"ok","checks":{"db":"ok","migrations":"ok"}}\n'
    exit 0
  fi
  echo "stub curl: not ready" >&2
  exit 22
  ;;
*/health)
  printf '{"status":"ok","pid":1,"commit":"%s","started_at":"2026-09-17T00:00:00Z"}\n' \
    "$(cat "${STUB_STATE_DIR:?}/health_commit" 2>/dev/null || echo unknown)"
  exit 0
  ;;
esac
exit 0
STUB

chmod +x "$stub_bin/docker" "$stub_bin/curl"

export STUB_LOG="$docker_log"
export STUB_STATE_DIR="$stub_state"
export STUB_OLD_COMMIT=""

run_status=0
run_script() {
  set +e
  PATH="$stub_bin:$PATH" \
    MULTICA_AUTOUPDATE_IMAGE_SOURCE="${IMAGE_SOURCE:-build}" \
    MULTICA_AUTOUPDATE_REPO_DIR="$checkout" \
    MULTICA_AUTOUPDATE_STATE_FILE="$state_dir/autoupdate.json" \
    MULTICA_AUTOUPDATE_LOCK_FILE="$state_dir/autoupdate.lock" \
    MULTICA_AUTOUPDATE_HEALTH_URL="http://127.0.0.1:8080" \
    MULTICA_AUTOUPDATE_WAIT_ATTEMPTS=2 \
    bash "$SCRIPT" >"$work/run.out" 2>&1
  run_status=$?
  set -e
}

state_file="$state_dir/autoupdate.json"

# ready/health answers as the box would report them before the run starts.
reset_run() {
  local ready=$1 health_commit=$2
  : >"$docker_log"
  rm -f "$state_file" "$state_dir/autoupdate.lock"
  mkdir -p "$stub_state"
  printf '%s' "$ready" >"$stub_state/ready"
  printf '%s' "$health_commit" >"$stub_state/health_commit"
}

# ---------------------------------------------------------------------------
# 1. No drift: the tip is what /health reports. Nothing gets built.
# ---------------------------------------------------------------------------
reset_run 1 "$sha_b"
git -C "$checkout" reset -q --hard "$sha_b"
run_script
require_eq "$run_status" 0 "no-drift run must exit 0"
require_contains "$state_file" '"result": "noop"' "no drift must be a noop"
require_contains "$state_file" "\"deployed_commit\": \"$sha_b\"" "noop must record what is deployed"
require_not_contains "$docker_log" "docker " "a noop must not touch Docker at all"
echo "ok: no drift is a noop"

# ---------------------------------------------------------------------------
# 2. Drift: build with the target SHA, recreate, /readyz and /health agree.
# ---------------------------------------------------------------------------
reset_run 1 "$sha_a"
git -C "$checkout" reset -q --hard "$sha_a"
run_script
require_eq "$run_status" 0 "a successful update must exit 0"
require_contains "$state_file" '"result": "updated"' "drift must report an update"
require_contains "$state_file" "\"target_commit\": \"$sha_b\"" "the target SHA must be recorded"
require_contains "$state_file" "\"deployed_commit\": \"$sha_b\"" "the deployed SHA must be the target"
require_contains "$docker_log" "build VERSION=$sha_b COMMIT=$sha_b" "the image must be built with the target commit"
require_contains "$docker_log" "recreate-update" "the new image must be recreated into a container"
require_eq "$(git -C "$checkout" rev-parse HEAD)" "$sha_b" "the checkout must end on the target commit"
echo "ok: drift builds with the target commit and reports updated"

# ---------------------------------------------------------------------------
# 3. The upgrade never becomes ready: roll back to :prev and to the old source.
# ---------------------------------------------------------------------------
reset_run 0 "$sha_a"
export STUB_OLD_COMMIT="$sha_a"
export STUB_READY_ON_UPDATE=0
export STUB_ROLLBACK_READY=1
export STUB_ROLLBACK_COMMIT="$sha_a"
git -C "$checkout" reset -q --hard "$sha_a"
run_script
require_eq "$run_status" 1 "a failed upgrade must exit non-zero"
require_contains "$state_file" '"result": "rolled_back"' "a failed readiness check must roll back"
require_contains "$docker_log" "docker tag multica-backend:prev multica-backend:dev" "the backend image must be restored from :prev"
require_contains "$docker_log" "docker tag multica-web:prev multica-web:dev" "the web image must be restored from :prev"
require_contains "$docker_log" "recreate-rollback" "the rollback must recreate the containers"
require_eq "$(git -C "$checkout" rev-parse HEAD)" "$sha_a" "the rollback must restore the old commit"
require_contains "$state_file" "\"deployed_commit\": \"$sha_a\"" "the rollback must record what is running again"
require_contains "$state_file" '"error"' "the failure must be recorded"
echo "ok: a stack that never becomes ready rolls back"

# ---------------------------------------------------------------------------
# 4. Built but not in effect: /health keeps answering with the old commit.
# ---------------------------------------------------------------------------
reset_run 1 "$sha_a"
export STUB_READY_ON_UPDATE=1
export STUB_UPDATE_COMMIT_VALUE="$sha_a"
export STUB_ROLLBACK_COMMIT="$sha_a"
git -C "$checkout" reset -q --hard "$sha_a"
run_script
require_eq "$run_status" 1 "a build that did not take effect must exit non-zero"
require_contains "$state_file" '"result": "rolled_back"' "a commit mismatch must roll back"
require_contains "$state_file" "did not take effect" "the recorded error must name the commit mismatch"
require_contains "$docker_log" "recreate-update" "the upgrade must have been attempted"
require_eq "$(git -C "$checkout" rev-parse HEAD)" "$sha_a" "the rollback must restore the old commit"
echo "ok: a build whose commit never reaches /health rolls back"

# ---------------------------------------------------------------------------
# 4b. Registry mode, images not published yet: pending, nothing touched.
# ---------------------------------------------------------------------------
reset_run 1 "$sha_a"
unset STUB_UPDATE_COMMIT_VALUE STUB_ROLLBACK_COMMIT
export STUB_OLD_COMMIT="" STUB_PULL_FAILS=1
git -C "$checkout" reset -q --hard "$sha_a"
IMAGE_SOURCE=registry run_script
require_eq "$run_status" 0 "waiting for CI is not a failure"
require_contains "$state_file" '"result": "pending"' "unpublished images must be pending"
require_contains "$state_file" "manifest unknown" "the pull error must be recorded"
require_contains "$docker_log" "docker pull ghcr.io/jeff-kunkun/multica-backend:sha-$sha_b" "the pull must name the target commit"
require_not_contains "$docker_log" "docker tag" "a pending run must not retag anything"
require_not_contains "$docker_log" "docker compose" "a pending run must not touch the stack"
require_eq "$(git -C "$checkout" rev-parse HEAD)" "$sha_a" "a pending run must not move the checkout"
echo "ok: registry mode waits for CI without touching the stack"

# ---------------------------------------------------------------------------
# 4c. Registry mode, images published: pull and retag, never build.
# ---------------------------------------------------------------------------
reset_run 1 "$sha_a"
export STUB_PULL_FAILS=0
git -C "$checkout" reset -q --hard "$sha_a"
IMAGE_SOURCE=registry run_script
require_eq "$run_status" 0 "a registry update must exit 0"
require_contains "$state_file" '"result": "updated"' "a registry update must report updated"
require_contains "$docker_log" "docker tag multica-backend:dev multica-backend:prev" "the rollback anchor must still be taken"
require_contains "$docker_log" "docker tag ghcr.io/jeff-kunkun/multica-web:sha-$sha_b multica-web:dev" "the pulled image must become :dev"
require_not_contains "$docker_log" " build" "registry mode must never build on the box"
require_contains "$docker_log" "recreate-update" "the pulled image must be recreated into a container"
echo "ok: registry mode pulls the target images instead of building"

# ---------------------------------------------------------------------------
# 5. Lock held by a live run: skip immediately, do not build, do not touch the
#    previous status.
# ---------------------------------------------------------------------------
reset_run 1 "$sha_b"
git -C "$checkout" reset -q --hard "$sha_b"
printf '%s\n' "$$" >"$state_dir/autoupdate.lock"
run_script
require_eq "$run_status" 0 "a locked-out tick is a skip, not a failure"
[ ! -f "$state_file" ] || fail "a skipped run must not rewrite the status file"
require_not_contains "$docker_log" "docker " "a skipped run must not touch Docker"
rm -f "$state_dir/autoupdate.lock"
echo "ok: a concurrent run is skipped, not stacked"

# ---------------------------------------------------------------------------
# Installer: renders the unit for this checkout, and a re-run is a no-op.
# ---------------------------------------------------------------------------
unit_dir="$work/units"
mkdir -p "$unit_dir"
bash "$INSTALLER" --repo-dir "$checkout" --branch kun --unit-dir "$unit_dir" \
  --state-dir "$work/inststate" --no-enable >"$work/install1.out" 2>&1
require_contains "$unit_dir/multica-autoupdate.service" "WorkingDirectory=$checkout" "the installer must point the unit at the checkout"
require_contains "$unit_dir/multica-autoupdate.service" "MULTICA_AUTOUPDATE_BRANCH=kun" "the installer must bake in the branch"
require_contains "$unit_dir/multica-autoupdate.timer" "OnUnitActiveSec=15min" "the timer must poll every 15 minutes"
require_contains "$unit_dir/multica-autoupdate.timer" "OnActiveSec=5min" "the timer must not fire the instant it is enabled"
require_contains "$unit_dir/multica-autoupdate.service" "MULTICA_AUTOUPDATE_STATE_FILE=$work/inststate/autoupdate.json" "--state-dir must reach the unit, not just mkdir the directory"
require_contains "$unit_dir/multica-autoupdate.service" "TimeoutStartSec=3600" "the unit must outlive a cold build"
before="$(cksum "$unit_dir/multica-autoupdate.service" "$unit_dir/multica-autoupdate.timer")"
bash "$INSTALLER" --repo-dir "$checkout" --branch kun --unit-dir "$unit_dir" \
  --state-dir "$work/inststate" --no-enable >"$work/install2.out" 2>&1
after="$(cksum "$unit_dir/multica-autoupdate.service" "$unit_dir/multica-autoupdate.timer")"
require_eq "$after" "$before" "re-running the installer must not rewrite the units"
require_contains "$work/install2.out" "unchanged: " "re-running the installer must take the unchanged path"
if bash "$INSTALLER" --repo-dir "$work" --unit-dir "$unit_dir" --no-enable >/dev/null 2>&1; then
  fail "the installer must refuse a directory that is not a self-host checkout"
fi
echo "ok: the installer is idempotent and rejects a bad checkout"

# ---------------------------------------------------------------------------
# 6. A fetch that cannot reach the origin fails the run without mutating
#    anything. Last, because it breaks the origin on purpose.
# ---------------------------------------------------------------------------
rm -rf "$origin"
reset_run 1 "$sha_b"
run_script
require_eq "$run_status" 1 "an unreachable origin must exit non-zero"
require_contains "$state_file" '"result": "failed"' "an unreachable origin must be recorded as failed"
require_not_contains "$docker_log" "docker compose" "a failed fetch must not build anything"
echo "ok: an unreachable origin fails without touching the stack"

echo ""
echo "✓ selfhost-autoupdate tests passed"
