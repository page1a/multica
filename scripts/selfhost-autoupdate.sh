#!/usr/bin/env bash
# Keep a self-hosted Multica checkout on the tip of an upstream branch.
#
# Why this exists: the box behind ai.ferryway.cc drifted 70 commits behind
# origin/kun and nobody noticed until an import failed with a confusing
# "unsupported transfer schema_version". Nothing on the machine compared what
# was answering requests against what was published, so the manual fix drifted
# again the same day. This script is that comparison, plus the rebuild, plus
# the rollback when the rebuild goes wrong.
#
# Design notes worth keeping:
#
#   - Drift is decided by the deployed binary's own /health commit, not by the
#     git worktree. The worktree says what was checked out; the binary says
#     what is answering requests. Compose ran for months without COMMIT, so
#     the two could disagree with nobody the wiser.
#   - The rollback anchor is a :prev image tag taken before the build, and the
#     recreate is what actually retires the bad build. Resetting git alone
#     leaves the containers on whatever image they were started from.
#   - Every exit path writes the status file, including the EXIT trap. A timer
#     whose state file goes stale is a timer nobody can debug.
#   - IMAGE_SOURCE=registry pulls <registry>/multica-{backend,web}:sha-<target>
#     (built by .github/workflows/selfhost-images.yml) instead of compiling on
#     the box, and retags them to the local :dev names, so the compose files,
#     :prev rollback and /health check stay exactly as in build mode. Images
#     that are not published yet are "pending": nothing is touched and the next
#     tick tries again.
#
# Usage: scripts/selfhost-autoupdate.sh
# Runbook: docs/kun/selfhost-autoupdate.md
set -euo pipefail

START_EPOCH=$(date +%s)
LAST_CHECK_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# Overridable so the unit can point at a checkout that is not this script's
# parent, and so the stubbed test can run the real script against a throwaway
# clone.
ROOT_DIR="${MULTICA_AUTOUPDATE_REPO_DIR:-$(cd "$SCRIPT_DIR/.." && pwd)}"

BRANCH="${MULTICA_AUTOUPDATE_BRANCH:-kun}"
STATE_FILE="${MULTICA_AUTOUPDATE_STATE_FILE:-/var/lib/multica/autoupdate.json}"
LOCK_FILE="${MULTICA_AUTOUPDATE_LOCK_FILE:-$(dirname "$STATE_FILE")/autoupdate.lock}"
# Generous by default: a cold frontend build plus migrations on a 4 GiB box is
# minutes, not seconds.
WAIT_ATTEMPTS="${MULTICA_AUTOUPDATE_WAIT_ATTEMPTS:-90}"
CURL_TIMEOUT="${MULTICA_AUTOUPDATE_CURL_TIMEOUT:-5}"
# These must match the tags docker-compose.selfhost.build.yml builds.
BACKEND_IMAGE="${MULTICA_AUTOUPDATE_BACKEND_IMAGE:-multica-backend:dev}"
FRONTEND_IMAGE="${MULTICA_AUTOUPDATE_FRONTEND_IMAGE:-multica-web:dev}"
# build: compile on this box (the original behaviour). registry: pull what CI
# published for the target commit.
IMAGE_SOURCE="${MULTICA_AUTOUPDATE_IMAGE_SOURCE:-build}"
REGISTRY="${MULTICA_AUTOUPDATE_REGISTRY:-ghcr.io/jeff-kunkun}"

STATE_WRITTEN=0
TARGET_COMMIT=""
DEPLOYED_COMMIT=""
HEAD_BEFORE=""
HAVE_BACKEND_PREV=0
HAVE_FRONTEND_PREV=0
STEP_LOG=""
STEP_NAME=""
STEP_ERROR=""

# The Makefile normalises the port aliases into PORT before a recipe runs, so
# a script that reads the chain itself sees a different answer. Ask Compose for
# the published port instead; that is the only authority on what is actually
# on the host. Same helper, same reason, as scripts/install.sh and
# scripts/selfhost-wait.sh.
read -r -a compose_cmd <<<"${COMPOSE:-docker compose}"
compose_files=(-f docker-compose.selfhost.yml -f docker-compose.selfhost.build.yml)

log() {
  printf '[%s] %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*" >&2
}

json_escape() {
  local value=$1
  value=${value//\\/\\\\}
  value=${value//\"/\\\"}
  value=${value//$'\n'/\\n}
  value=${value//$'\r'/\\r}
  value=${value//$'\t'/\\t}
  printf '%s' "$value"
}

write_state() {
  local result=$1 error_text=$2 state_dir tmp_path
  state_dir="$(dirname "$STATE_FILE")"
  mkdir -p "$state_dir" 2>/dev/null || true
  tmp_path="$STATE_FILE.tmp.$$"
  {
    printf '{\n'
    printf '  "last_check_at": "%s",\n' "$LAST_CHECK_AT"
    printf '  "deployed_commit": "%s",\n' "$DEPLOYED_COMMIT"
    printf '  "target_commit": "%s",\n' "$TARGET_COMMIT"
    printf '  "result": "%s",\n' "$result"
    printf '  "error": "%s",\n' "$(json_escape "$error_text")"
    printf '  "duration_seconds": %s\n' "$(( $(date +%s) - START_EPOCH ))"
    printf '}\n'
  } >"$tmp_path"
  mv "$tmp_path" "$STATE_FILE"
  STATE_WRITTEN=1
}

# The single exit point for the whole run: writes the state the issue asks for
# and picks the exit code the timer will surface in the journal.
finish() {
  local result=$1 error_text=${2:-}
  write_state "$result" "$error_text"
  log "result=$result ${error_text:+error=$error_text}"
  case "$result" in
  noop | updated | pending) exit 0 ;;
  *) exit 1 ;;
  esac
}

cleanup() {
  local status=$?
  if [ -n "$STEP_LOG" ]; then rm -f "$STEP_LOG" 2>/dev/null || true; fi
  rm -f "$LOCK_FILE" 2>/dev/null || true
  if [ "$STATE_WRITTEN" -eq 0 ]; then
    # Something exited outside finish(): a command set -e did not guard, a
    # signal, or a bug. Record it rather than leaving the last green run on
    # disk pretending nothing happened.
    write_state failed "run ended unexpectedly (status $status) before a result was recorded" || true
  fi
  exit "$status"
}

# Hard-link lock: ln(1) is atomic and fails with EEXIST, so two runs that race
# to create the lock cannot both win the way a "test -f, then create" pair can.
# Two runs that race the *stale* removal can still both win in principle; that
# needs a crashed previous run within the same instant as the next one, and the
# cost is two builds converging on the same commit.
acquire_lock() {
  local candidate="$LOCK_FILE.candidate.$$" holder=""
  mkdir -p "$(dirname "$LOCK_FILE")" 2>/dev/null || true
  printf '%s\n' "$$" >"$candidate" 2>/dev/null || return 1
  if ln "$candidate" "$LOCK_FILE" 2>/dev/null; then
    rm -f "$candidate"
    return 0
  fi
  rm -f "$candidate"

  if [ -f "$LOCK_FILE" ]; then holder="$(cat "$LOCK_FILE" 2>/dev/null || true)"; fi
  if [ -n "$holder" ] && kill -0 "$holder" 2>/dev/null; then
    log "lock held by live pid $holder"
    return 1
  fi

  log "removing stale lock $LOCK_FILE (holder '${holder:-unknown}' is not running)"
  rm -f "$LOCK_FILE" 2>/dev/null || true
  printf '%s\n' "$$" >"$candidate" 2>/dev/null || return 1
  if ln "$candidate" "$LOCK_FILE" 2>/dev/null; then
    rm -f "$candidate"
    return 0
  fi
  rm -f "$candidate"
  return 1
}

# Run a step with its output both on the journal and in $STEP_LOG, so a failure
# can quote the original text in the status file instead of a summary.
run_step() {
  STEP_NAME=$1
  shift
  : >"$STEP_LOG"
  if "$@" 2>&1 | tee "$STEP_LOG"; then
    return 0
  fi
  STEP_ERROR="$(tail -n 20 "$STEP_LOG" 2>/dev/null || true)"
  return 1
}

step_error_text() {
  printf '%s failed: %s' "$STEP_NAME" "$(printf '%s' "$STEP_ERROR" | tr '\n' '~')"
}

health_url() {
  if [ -n "${MULTICA_AUTOUPDATE_HEALTH_URL:-}" ]; then
    printf '%s' "${MULTICA_AUTOUPDATE_HEALTH_URL%/}"
    return 0
  fi
  local port
  port="$(compose_host_port backend 8080 "${BACKEND_PORT:-${API_PORT:-${SERVER_PORT:-${PORT:-8080}}}}")"
  printf 'http://127.0.0.1:%s' "$port"
}

compose_host_port() {
  local service=$1 container_port=$2 fallback=$3 published=""
  published="$("${compose_cmd[@]}" "${compose_files[@]}" port "$service" "$container_port" 2>/dev/null | tail -n 1)" || published=""
  published=${published##*:}
  published=${published%$'\r'}
  case "$published" in
  '' | *[!0-9]*) printf '%s\n' "$fallback" ;;
  *) printf '%s\n' "$published" ;;
  esac
}

# The commit the running backend reports about itself. Empty when the backend
# is down or too old to answer with a commit, which counts as drift: the point
# is to converge on a state the machine can prove.
health_commit() {
  local body=""
  body="$(curl -fsS --max-time "$CURL_TIMEOUT" "$HEALTH_URL/health" 2>/dev/null)" || return 1
  printf '%s' "$body" | sed -n 's/.*"commit"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p'
}

# Readiness, not liveness, and through the shared wait script so the published
# port has one owner. WAIT_STRICT is what turns its "still starting" advice
# into a failure this script can roll back on.
wait_ready() {
  WAIT_PATH=/readyz WAIT_STRICT=1 WAIT_ATTEMPTS="$WAIT_ATTEMPTS" \
    bash "$ROOT_DIR/scripts/selfhost-wait.sh" build
}

# multica-backend:dev -> multica-backend:prev. The tag is replaced, not
# appended: "multica-backend:dev:prev" is not a legal image reference, and the
# rollback anchor only has to be the same repository at an older tag.
prev_tag() {
  case "${1##*/}" in
  *:*) printf '%s:prev' "${1%:*}" ;;
  *) printf '%s:prev' "$1" ;;
  esac
}

retag_prev() {
  local image=$1 label=$2 prev
  prev="$(prev_tag "$image")"
  if docker image inspect "$image" >/dev/null 2>&1; then
    if ! run_step "docker tag $image $prev" docker tag "$image" "$prev"; then
      log "warning: could not tag $prev ($label); rollback will rebuild from the previous commit"
      return 1
    fi
    return 0
  fi
  log "warning: $image is not present; rollback will rebuild from the previous commit"
  return 1
}

restore_prev() {
  local image=$1 had_prev=$2 prev
  prev="$(prev_tag "$image")"
  if [ "$had_prev" = "1" ]; then
    if ! run_step "docker tag $prev $image" docker tag "$prev" "$image"; then
      finish failed "rollback: could not restore $image from $prev; $REASON; $(step_error_text)"
    fi
  fi
}

# Restore what was running before this run: images, source, containers, then
# proof that the previous version answers again. A rollback that cannot be
# verified is reported as failed, not as rolled_back.
rollback() {
  REASON=$1
  log "rolling back: $REASON"
  # A rebuild triggered here (only when :prev is missing) must be labelled with
  # the commit it is actually built from, or /health would lie in exactly the
  # way this script exists to prevent.
  export VERSION="$HEAD_BEFORE" COMMIT="$HEAD_BEFORE" DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

  restore_prev "$BACKEND_IMAGE" "$HAVE_BACKEND_PREV"
  restore_prev "$FRONTEND_IMAGE" "$HAVE_FRONTEND_PREV"

  if ! run_step "git reset --hard $HEAD_BEFORE" git reset --hard "$HEAD_BEFORE"; then
    finish failed "rollback: could not reset to $HEAD_BEFORE; $REASON; $(step_error_text)"
  fi
  if ! run_step "docker compose up -d (rollback)" "${compose_cmd[@]}" "${compose_files[@]}" up -d --force-recreate backend frontend; then
    finish failed "rollback: could not recreate the previous containers; $REASON; $(step_error_text)"
  fi
  if ! run_step "waiting for /readyz after rollback" wait_ready; then
    finish failed "rollback: the previous version did not become ready; $REASON; $(step_error_text)"
  fi

  DEPLOYED_COMMIT="$(health_commit || true)"
  if [ -z "$DEPLOYED_COMMIT" ]; then DEPLOYED_COMMIT="$HEAD_BEFORE"; fi
  finish rolled_back "$REASON"
}

registry_ref() {
  printf '%s/%s:sha-%s' "$REGISTRY" "$1" "$TARGET_COMMIT"
}

# Pull both images before anything is mutated. A missing tag is the normal
# "CI has not finished this commit yet" case, so it ends the run as pending
# with the pull output kept for when it is not.
pull_target_images() {
  local name
  for name in multica-backend multica-web; do
    if ! run_step "docker pull $(registry_ref "$name")" docker pull "$(registry_ref "$name")"; then
      finish pending "images for $TARGET_COMMIT are not pullable yet: $(step_error_text)"
    fi
  done
}

# Point the local :dev names at the pulled images, then drop the registry tags
# and anything left dangling, so the box keeps only :dev and :prev.
adopt_target_images() {
  run_step "docker tag $(registry_ref multica-backend) $BACKEND_IMAGE" \
    docker tag "$(registry_ref multica-backend)" "$BACKEND_IMAGE" &&
    run_step "docker tag $(registry_ref multica-web) $FRONTEND_IMAGE" \
      docker tag "$(registry_ref multica-web)" "$FRONTEND_IMAGE"
}

prune_images() {
  docker rmi "$(registry_ref multica-backend)" "$(registry_ref multica-web)" >/dev/null 2>&1 || true
  docker image prune -f >/dev/null 2>&1 || true
}

main() {
  case "$IMAGE_SOURCE" in
  build | registry) ;;
  *)
    log "MULTICA_AUTOUPDATE_IMAGE_SOURCE must be build or registry, got '$IMAGE_SOURCE'"
    exit 1
    ;;
  esac
  if [ ! -f "$ROOT_DIR/docker-compose.selfhost.yml" ]; then
    log "no docker-compose.selfhost.yml under $ROOT_DIR; set MULTICA_AUTOUPDATE_REPO_DIR"
    exit 1
  fi
  mkdir -p "$(dirname "$STATE_FILE")" 2>/dev/null || {
    log "cannot create state directory $(dirname "$STATE_FILE")"
    exit 1
  }

  if ! acquire_lock; then
    # Skipping is not a failure: the run that holds the lock is doing the work
    # and will write the state file. Leave the previous state in place.
    log "another autoupdate run is in progress; skipping this tick"
    exit 0
  fi
  trap cleanup EXIT
  STEP_LOG="$(mktemp "${TMPDIR:-/tmp}/multica-autoupdate.XXXXXX")"

  cd "$ROOT_DIR"
  HEALTH_URL="$(health_url)"

  DEPLOYED_COMMIT="$(health_commit || true)"

  if ! run_step "git fetch --prune origin $BRANCH" git fetch --prune origin "$BRANCH"; then
    finish failed "$(step_error_text)"
  fi
  if ! TARGET_COMMIT="$(git rev-parse --verify "origin/$BRANCH" 2>/dev/null)"; then
    finish failed "origin/$BRANCH did not resolve after a successful fetch"
  fi

  if [ -n "$DEPLOYED_COMMIT" ] && [ "$TARGET_COMMIT" = "$DEPLOYED_COMMIT" ]; then
    log "no drift: $BRANCH tip $TARGET_COMMIT is already deployed"
    finish noop ""
  fi

  log "drift: deployed '${DEPLOYED_COMMIT:-unknown}' target '$TARGET_COMMIT'"
  HEAD_BEFORE="$(git rev-parse HEAD)"

  # git reset --hard silently destroys tracked edits, and this runs unattended.
  # Untracked files are left alone by reset, so they are not a reason to stop.
  dirty="$(git status --porcelain --untracked-files=no)"
  if [ -n "$dirty" ]; then
    finish failed "tracked files are modified in $ROOT_DIR; refusing to reset --hard: $(printf '%s' "$dirty" | tr '\n' ' ')"
  fi

  if [ "$IMAGE_SOURCE" = registry ]; then pull_target_images; fi

  if retag_prev "$BACKEND_IMAGE" backend; then HAVE_BACKEND_PREV=1; fi
  if retag_prev "$FRONTEND_IMAGE" frontend; then HAVE_FRONTEND_PREV=1; fi

  # The tags above are the anchor for this reset: without them the old source
  # is gone the moment the worktree moves.
  if ! run_step "git reset --hard origin/$BRANCH" git reset --hard "origin/$BRANCH"; then
    rollback "could not check out $TARGET_COMMIT: $(step_error_text)"
  fi

  # The build args are the whole reason /health can be trusted afterwards:
  # the image records the SHA it was built from, and step 5 checks that the
  # running container agrees.
  export VERSION="$TARGET_COMMIT" COMMIT="$TARGET_COMMIT" DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

  if [ "$IMAGE_SOURCE" = registry ]; then
    if ! adopt_target_images; then
      rollback "could not tag the pulled images: $(step_error_text)"
    fi
  elif ! run_step "docker compose build" "${compose_cmd[@]}" "${compose_files[@]}" build; then
    rollback "build failed: $(step_error_text)"
  fi
  if ! run_step "docker compose up -d" "${compose_cmd[@]}" "${compose_files[@]}" up -d --force-recreate backend frontend; then
    rollback "docker compose up failed: $(step_error_text)"
  fi
  if ! run_step "waiting for /readyz" wait_ready; then
    rollback "the upgraded stack never became ready: $(step_error_text)"
  fi

  DEPLOYED_COMMIT="$(health_commit || true)"
  if [ "$DEPLOYED_COMMIT" != "$TARGET_COMMIT" ]; then
    rollback "/health reports '${DEPLOYED_COMMIT:-unknown}' but $TARGET_COMMIT was built (the build did not take effect)"
  fi

  if [ "$IMAGE_SOURCE" = registry ]; then prune_images; fi

  log "updated to $TARGET_COMMIT"
  finish updated ""
}

main "$@"
