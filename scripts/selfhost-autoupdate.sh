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
#   - MULTICA_AUTOUPDATE_SWITCH=nginx (DENE-1617) replaces the recreate with a
#     blue/green cutover: the new pair starts on the idle colour's ports while
#     the old pair keeps serving, nginx is pointed at it only once /readyz and
#     /health agree, and the old pair is stopped after that. A failure before
#     the flip leaves the old pair untouched, so "rollback" is just removing
#     the new containers. Migrations the old code cannot live with (see
#     migration_blockers) fall back to stop-then-start, said so in the log.
#   - MULTICA_AUTOUPDATE_QUIET_SECONDS holds a deploy while kun is still
#     moving, but never past MULTICA_AUTOUPDATE_MAX_DELAY_SECONDS after the
#     oldest undeployed commit, so a burst of merges is one cutover.
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

# recreate: stop and start the one pair (the original behaviour). nginx:
# blue/green cutover through an nginx upstream file this script owns.
SWITCH_MODE="${MULTICA_AUTOUPDATE_SWITCH:-recreate}"
UPSTREAM_FILE="${MULTICA_AUTOUPDATE_NGINX_UPSTREAM_FILE:-/etc/nginx/conf.d/multica-upstream.conf}"
read -r -a nginx_test_cmd <<<"${MULTICA_AUTOUPDATE_NGINX_TEST:-nginx -t}"
read -r -a nginx_reload_cmd <<<"${MULTICA_AUTOUPDATE_NGINX_RELOAD:-nginx -s reload}"
read -r -a nginx_dump_cmd <<<"${MULTICA_AUTOUPDATE_NGINX_DUMP:-nginx -T}"
# Time between the nginx reload and SIGTERM to the old pair: reload is
# asynchronous, and requests already inside old workers finish against the old
# backend. The backend's own shutdown then drains HTTP and drops its sockets,
# and every daemon and browser resyncs on reconnect.
DRAIN_SECONDS="${MULTICA_AUTOUPDATE_DRAIN_SECONDS:-5}"
STOP_TIMEOUT="${MULTICA_AUTOUPDATE_STOP_TIMEOUT:-60}"
# A drill knob: point readiness at a path that 404s to rehearse a failed new
# version without breaking one.
READY_PATH="${MULTICA_AUTOUPDATE_READY_PATH:-/readyz}"
QUIET_SECONDS="${MULTICA_AUTOUPDATE_QUIET_SECONDS:-0}"
MAX_DELAY_SECONDS="${MULTICA_AUTOUPDATE_MAX_DELAY_SECONDS:-1800}"
# Redeploy even without drift; for cutover drills.
FORCE="${MULTICA_AUTOUPDATE_FORCE:-0}"
export MULTICA_GREEN_BACKEND_PORT="${MULTICA_GREEN_BACKEND_PORT:-8081}"
export MULTICA_GREEN_FRONTEND_PORT="${MULTICA_GREEN_FRONTEND_PORT:-3001}"
BLUE_BACKEND_PORT="${BACKEND_PORT:-${API_PORT:-${SERVER_PORT:-${PORT:-8080}}}}"
BLUE_FRONTEND_PORT="${FRONTEND_PORT:-3000}"

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
  local base="${1:-$HEALTH_URL}" body=""
  body="$(curl -fsS --max-time "$CURL_TIMEOUT" "$base/health" 2>/dev/null)" || return 1
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

# ---------------------------------------------------------------------------
# Blue/green (MULTICA_AUTOUPDATE_SWITCH=nginx)
# ---------------------------------------------------------------------------

bluegreen_files=(-f docker-compose.selfhost.yml -f docker-compose.selfhost.build.yml -f docker-compose.selfhost.bluegreen.yml)

backend_service() { if [ "$1" = green ]; then echo backend-green; else echo backend; fi; }
frontend_service() { if [ "$1" = green ]; then echo frontend-green; else echo frontend; fi; }
other_color() { if [ "$1" = green ]; then echo blue; else echo green; fi; }

backend_port() {
  if [ "$1" = green ]; then echo "$MULTICA_GREEN_BACKEND_PORT"; else echo "$BLUE_BACKEND_PORT"; fi
}
frontend_port() {
  if [ "$1" = green ]; then echo "$MULTICA_GREEN_FRONTEND_PORT"; else echo "$BLUE_FRONTEND_PORT"; fi
}

# The upstream file is the one record of which colour nginx sends traffic to.
# No file yet means nothing has been switched, which is blue.
active_color() {
  local color=""
  if [ -f "$UPSTREAM_FILE" ]; then
    color="$(sed -n 's/^# multica-active-color: *\([a-z]*\).*/\1/p' "$UPSTREAM_FILE" | head -n 1)"
  fi
  case "$color" in
  green) echo green ;;
  *) echo blue ;;
  esac
}

# Active colour first, the other as backup: nginx only falls through to the
# backup when the primary refuses the connection, which covers the instant
# between a reload and the old workers retiring. max_fails=0 keeps one slow
# upstream response from benching the primary for everyone.
render_upstream() {
  local active=$1 standby
  standby="$(other_color "$active")"
  printf '# Managed by scripts/selfhost-autoupdate.sh (DENE-1617). Rewritten on every\n'
  printf '# cutover; edit the script, not this file.\n'
  printf '# multica-active-color: %s\n' "$active"
  printf 'upstream multica_backend {\n'
  printf '    server 127.0.0.1:%s max_fails=0;\n' "$(backend_port "$active")"
  printf '    server 127.0.0.1:%s backup;\n' "$(backend_port "$standby")"
  printf '}\n'
  printf 'upstream multica_frontend {\n'
  printf '    server 127.0.0.1:%s max_fails=0;\n' "$(frontend_port "$active")"
  printf '    server 127.0.0.1:%s backup;\n' "$(frontend_port "$standby")"
  printf '}\n'
}

# Write, test, reload. A config nginx rejects is put back before anything
# reloads, so a bad write can never take the site down.
switch_upstream() {
  local color=$1 backup="$UPSTREAM_FILE.prev.$$"
  if [ -f "$UPSTREAM_FILE" ]; then cp "$UPSTREAM_FILE" "$backup"; fi
  mkdir -p "$(dirname "$UPSTREAM_FILE")" 2>/dev/null || true
  render_upstream "$color" >"$UPSTREAM_FILE.tmp.$$" && mv "$UPSTREAM_FILE.tmp.$$" "$UPSTREAM_FILE"
  if ! run_step "nginx -t ($color)" "${nginx_test_cmd[@]}"; then
    if [ -f "$backup" ]; then mv "$backup" "$UPSTREAM_FILE"; else rm -f "$UPSTREAM_FILE"; fi
    return 1
  fi
  rm -f "$backup"
  run_step "nginx reload ($color)" "${nginx_reload_cmd[@]}"
}

# Blue/green only works when nginx goes through the upstream names. A site that
# still proxies straight to a port would keep sending traffic to the old pair
# after the "switch", and stopping it would be the outage this mode exists to
# avoid, so that is checked against what nginx actually loaded.
nginx_ready_for_bluegreen() {
  local dump direct
  dump="$("${nginx_dump_cmd[@]}" 2>/dev/null)" || {
    BLUEGREEN_REASON="could not read the nginx config (${nginx_dump_cmd[*]})"
    return 1
  }
  if ! printf '%s\n' "$dump" | grep -q 'upstream multica_backend'; then
    BLUEGREEN_REASON="nginx does not load $UPSTREAM_FILE (no upstream multica_backend)"
    return 1
  fi
  direct="$(printf '%s\n' "$dump" | grep -E "proxy_pass +https?://(127\.0\.0\.1|localhost):($BLUE_BACKEND_PORT|$MULTICA_GREEN_BACKEND_PORT|$BLUE_FRONTEND_PORT|$MULTICA_GREEN_FRONTEND_PORT)\b" | head -n 3 | tr -s ' ' | tr '\n' '|')"
  if [ -n "$direct" ]; then
    BLUEGREEN_REASON="nginx still proxies straight to a port: $direct"
    return 1
  fi
  return 0
}

# Pending migrations the previous version cannot run against: anything that
# removes, renames or retypes what old code reads or writes. Adding tables,
# columns with defaults, indexes and widening CHECKs are fine. A file can
# overrule the scan either way with a comment line:
#   -- zero-downtime: unsafe   (e.g. a CHECK that narrows what old code writes)
#   -- zero-downtime: safe     (e.g. dropping a table no released version reads)
# Prints one "file: reason" per blocker; an unknown deployed commit is itself a
# blocker, since there is nothing to diff against.
migration_blockers() {
  local from=$1 to=$2 file body
  if [ -z "$from" ] || ! git cat-file -e "$from^{commit}" 2>/dev/null; then
    echo "deployed commit '${from:-unknown}' is not in this checkout; cannot tell which migrations are new"
    return 0
  fi
  git diff --name-only --diff-filter=AM "$from" "$to" -- 'server/migrations/*.up.sql' | while read -r file; do
    [ -n "$file" ] || continue
    body="$(git show "$to:$file" 2>/dev/null | tr '[:upper:]' '[:lower:]')"
    if printf '%s\n' "$body" | grep -Eq '^--[[:space:]]*zero-downtime:[[:space:]]*unsafe'; then
      echo "$file: marked zero-downtime: unsafe"
      continue
    fi
    if printf '%s\n' "$body" | grep -Eq '^--[[:space:]]*zero-downtime:[[:space:]]*safe'; then
      continue
    fi
    body="$(printf '%s\n' "$body" | sed 's/--.*$//')"
    if printf '%s\n' "$body" | grep -Eq 'drop[[:space:]]+(table|column|view|materialized[[:space:]]+view|type|function|schema)'; then
      echo "$file: drops something the old version may still use"
    elif printf '%s\n' "$body" | grep -Eq 'rename[[:space:]]+(to|column)|alter[[:space:]]+table[^;]*[[:space:]]rename[[:space:]]'; then
      echo "$file: renames a table or column"
    elif printf '%s\n' "$body" | grep -Eq 'alter[[:space:]]+column[^;]*[[:space:]]type[[:space:]]'; then
      echo "$file: changes a column type"
    elif printf '%s\n' "$body" | grep -Eq 'set[[:space:]]+not[[:space:]]+null'; then
      echo "$file: adds NOT NULL to an existing column"
    elif printf '%s\n' "$body" | tr '\n' ' ' | grep -Eo 'add[[:space:]]+column[^,;]*' | grep 'not[[:space:]]\{1,\}null' | grep -vq 'default'; then
      echo "$file: adds a NOT NULL column without a default"
    fi
  done
}

wait_backend_ready() {
  local port=$1 i
  for i in $(seq 1 "$WAIT_ATTEMPTS"); do
    if curl -fsS --max-time "$CURL_TIMEOUT" "http://127.0.0.1:$port$READY_PATH" >/dev/null 2>&1; then
      echo "backend on :$port answers $READY_PATH"
      return 0
    fi
    sleep 2
  done
  echo "backend on :$port did not answer $READY_PATH after $WAIT_ATTEMPTS attempts"
  return 1
}

# Next answers / with a page or a redirect; anything but a refused connection
# or a 5xx means it is serving.
wait_frontend_ready() {
  local port=$1 i code
  for i in $(seq 1 "$WAIT_ATTEMPTS"); do
    code="$(curl -s -o /dev/null -w '%{http_code}' --max-time "$CURL_TIMEOUT" "http://127.0.0.1:$port/" 2>/dev/null || true)"
    case "$code" in
    [234][0-9][0-9])
      echo "frontend on :$port answers $code"
      return 0
      ;;
    esac
    sleep 2
  done
  echo "frontend on :$port never answered below 500 (last: ${code:-none})"
  return 1
}

start_color() {
  local color=$1
  run_step "docker compose up $color" "${compose_cmd[@]}" "${bluegreen_files[@]}" up -d --no-deps --force-recreate \
    "$(backend_service "$color")" "$(frontend_service "$color")"
}

stop_color() {
  local color=$1
  run_step "docker compose stop $color" "${compose_cmd[@]}" "${bluegreen_files[@]}" stop -t "$STOP_TIMEOUT" \
    "$(backend_service "$color")" "$(frontend_service "$color")"
}

# The idle pair failed before nginx ever pointed at it: drop it, put the tags
# and the checkout back, and the old pair has served throughout.
abandon_idle() {
  local idle=$1 reason=$2
  REASON=$reason
  log "abandoning the $idle pair: $reason"
  stop_color "$idle" || log "warning: could not stop the $idle pair: $(step_error_text)"
  restore_prev "$BACKEND_IMAGE" "$HAVE_BACKEND_PREV"
  restore_prev "$FRONTEND_IMAGE" "$HAVE_FRONTEND_PREV"
  if ! run_step "git reset --hard $HEAD_BEFORE" git reset --hard "$HEAD_BEFORE"; then
    finish failed "rollback: could not reset to $HEAD_BEFORE; $reason; $(step_error_text)"
  fi
  DEPLOYED_COMMIT="$(health_commit "http://127.0.0.1:$(backend_port "$ACTIVE")" || true)"
  if [ -z "$DEPLOYED_COMMIT" ]; then
    finish failed "the $ACTIVE pair stopped answering while the update was abandoned; $reason"
  fi
  finish rolled_back "$reason"
}

# Stop-then-start, for migrations the old version cannot survive. Same colours
# and the same nginx switch, so the next tick is a normal cutover again.
deploy_with_downtime() {
  local idle=$1 why=$2
  log "zero-downtime cutover not possible, stopping the $ACTIVE pair first: $why"
  if ! stop_color "$ACTIVE"; then
    abandon_idle "$idle" "could not stop the $ACTIVE pair: $(step_error_text)"
  fi
  if start_color "$idle" && run_step "waiting for the $idle backend" wait_backend_ready "$(backend_port "$idle")" &&
    run_step "waiting for the $idle frontend" wait_frontend_ready "$(frontend_port "$idle")"; then
    DEPLOYED_COMMIT="$(health_commit "http://127.0.0.1:$(backend_port "$idle")" || true)"
    if [ "$DEPLOYED_COMMIT" = "$TARGET_COMMIT" ] && switch_upstream "$idle"; then
      return 0
    fi
  fi
  local reason="the $idle pair did not come up after a downtime deploy: $(step_error_text)"
  log "restarting the $ACTIVE pair: $reason"
  stop_color "$idle" || true
  restore_prev "$BACKEND_IMAGE" "$HAVE_BACKEND_PREV"
  restore_prev "$FRONTEND_IMAGE" "$HAVE_FRONTEND_PREV"
  run_step "git reset --hard $HEAD_BEFORE" git reset --hard "$HEAD_BEFORE" || true
  # The stopped containers still hold the old image, so start (not up) is the
  # rollback: nothing is rebuilt or re-tagged.
  if ! run_step "docker compose start $ACTIVE" "${compose_cmd[@]}" "${bluegreen_files[@]}" start \
    "$(backend_service "$ACTIVE")" "$(frontend_service "$ACTIVE")"; then
    finish failed "rollback: could not restart the $ACTIVE pair; $reason; $(step_error_text)"
  fi
  if ! run_step "waiting for the $ACTIVE backend after rollback" wait_backend_ready "$(backend_port "$ACTIVE")"; then
    finish failed "rollback: the $ACTIVE pair did not become ready; $reason; $(step_error_text)"
  fi
  DEPLOYED_COMMIT="$(health_commit "http://127.0.0.1:$(backend_port "$ACTIVE")" || true)"
  finish rolled_back "$reason"
}

deploy_bluegreen() {
  local idle blockers reported
  idle="$(other_color "$ACTIVE")"

  blockers="$(migration_blockers "$DEPLOYED_COMMIT" "$TARGET_COMMIT")"
  if [ -n "$blockers" ]; then
    deploy_with_downtime "$idle" "migrations the running version cannot use: $(printf '%s' "$blockers" | tr '\n' ';')"
  else
    log "cutover: $ACTIVE -> $idle (old pair keeps serving until the new one is ready)"
    if ! start_color "$idle"; then
      abandon_idle "$idle" "docker compose up failed: $(step_error_text)"
    fi
    if ! run_step "waiting for the $idle backend" wait_backend_ready "$(backend_port "$idle")"; then
      abandon_idle "$idle" "the new backend never became ready: $(step_error_text)"
    fi
    if ! run_step "waiting for the $idle frontend" wait_frontend_ready "$(frontend_port "$idle")"; then
      abandon_idle "$idle" "the new frontend never became ready: $(step_error_text)"
    fi
    reported="$(health_commit "http://127.0.0.1:$(backend_port "$idle")" || true)"
    if [ "$reported" != "$TARGET_COMMIT" ]; then
      abandon_idle "$idle" "/health reports '${reported:-unknown}' but $TARGET_COMMIT was deployed (the build did not take effect)"
    fi
    DEPLOYED_COMMIT="$reported"
    if ! switch_upstream "$idle"; then
      abandon_idle "$idle" "nginx refused the switch: $(step_error_text)"
    fi
    sleep "$DRAIN_SECONDS"
    # Still healthy after taking real traffic? If not, the old pair is still
    # running: point nginx back before it goes anywhere.
    if ! curl -fsS --max-time "$CURL_TIMEOUT" "http://127.0.0.1:$(backend_port "$idle")$READY_PATH" >/dev/null 2>&1; then
      switch_upstream "$ACTIVE" || finish failed "the $idle backend failed after the switch and nginx could not be pointed back at $ACTIVE: $(step_error_text)"
      abandon_idle "$idle" "the $idle backend stopped answering $READY_PATH right after the switch"
    fi
    if ! stop_color "$ACTIVE"; then
      log "warning: the $ACTIVE pair did not stop cleanly; nginx no longer sends it traffic: $(step_error_text)"
    fi
  fi
  ACTIVE="$idle"
}

# Hold while the branch is still moving, so a burst of merges becomes one
# cutover instead of ten. Never hold past MAX_DELAY_SECONDS after the oldest
# commit that is not deployed yet.
quiet_gate() {
  local now tip_time oldest_time
  [ "$QUIET_SECONDS" -gt 0 ] 2>/dev/null || return 0
  now="$(date +%s)"
  tip_time="$(git log -1 --format=%ct "$TARGET_COMMIT" 2>/dev/null || echo 0)"
  [ $((now - tip_time)) -lt "$QUIET_SECONDS" ] || return 0
  oldest_time="$tip_time"
  if [ -n "$DEPLOYED_COMMIT" ] && git cat-file -e "$DEPLOYED_COMMIT^{commit}" 2>/dev/null; then
    oldest_time="$(git log --format=%ct "$DEPLOYED_COMMIT..$TARGET_COMMIT" 2>/dev/null | tail -n 1)"
    oldest_time="${oldest_time:-$tip_time}"
  fi
  if [ $((now - oldest_time)) -ge "$MAX_DELAY_SECONDS" ]; then
    log "kun is still moving, but the oldest undeployed commit is $((now - oldest_time))s old; deploying now"
    return 0
  fi
  finish pending "waiting for $BRANCH to settle: tip is $((now - tip_time))s old, deploying once it is ${QUIET_SECONDS}s old (at the latest $((MAX_DELAY_SECONDS - (now - oldest_time)))s from now)"
}

main() {
  case "$IMAGE_SOURCE" in
  build | registry) ;;
  *)
    log "MULTICA_AUTOUPDATE_IMAGE_SOURCE must be build or registry, got '$IMAGE_SOURCE'"
    exit 1
    ;;
  esac
  case "$SWITCH_MODE" in
  recreate | nginx) ;;
  *)
    log "MULTICA_AUTOUPDATE_SWITCH must be recreate or nginx, got '$SWITCH_MODE'"
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
  ACTIVE=blue
  USE_BLUEGREEN=0
  HEALTH_URL_DERIVED=0
  if [ "$SWITCH_MODE" = nginx ]; then
    # First run: give nginx something to include before its sites are pointed
    # at the upstream names (docs/kun/selfhost-autoupdate.md). Nothing reloads.
    if [ ! -f "$UPSTREAM_FILE" ]; then
      mkdir -p "$(dirname "$UPSTREAM_FILE")" 2>/dev/null || true
      render_upstream blue >"$UPSTREAM_FILE"
      log "wrote $UPSTREAM_FILE for the blue pair; point the nginx sites at multica_backend / multica_frontend"
    fi
    ACTIVE="$(active_color)"
    if [ -z "${MULTICA_AUTOUPDATE_HEALTH_URL:-}" ]; then
      HEALTH_URL_DERIVED=1
      MULTICA_AUTOUPDATE_HEALTH_URL="http://127.0.0.1:$(backend_port "$ACTIVE")"
    fi
  fi
  HEALTH_URL="$(health_url)"

  DEPLOYED_COMMIT="$(health_commit || true)"

  if ! run_step "git fetch --prune origin $BRANCH" git fetch --prune origin "$BRANCH"; then
    finish failed "$(step_error_text)"
  fi
  if ! TARGET_COMMIT="$(git rev-parse --verify "origin/$BRANCH" 2>/dev/null)"; then
    finish failed "origin/$BRANCH did not resolve after a successful fetch"
  fi

  if [ -n "$DEPLOYED_COMMIT" ] && [ "$TARGET_COMMIT" = "$DEPLOYED_COMMIT" ]; then
    if [ "$FORCE" != "1" ]; then
      log "no drift: $BRANCH tip $TARGET_COMMIT is already deployed"
      finish noop ""
    fi
    log "no drift, but MULTICA_AUTOUPDATE_FORCE=1: redeploying $TARGET_COMMIT"
  else
    log "drift: deployed '${DEPLOYED_COMMIT:-unknown}' target '$TARGET_COMMIT'"
    quiet_gate
  fi
  HEAD_BEFORE="$(git rev-parse HEAD)"

  # git reset --hard silently destroys tracked edits, and this runs unattended.
  # Untracked files are left alone by reset, so they are not a reason to stop.
  dirty="$(git status --porcelain --untracked-files=no)"
  if [ -n "$dirty" ]; then
    finish failed "tracked files are modified in $ROOT_DIR; refusing to reset --hard: $(printf '%s' "$dirty" | tr '\n' ' ')"
  fi

  if [ "$SWITCH_MODE" = nginx ]; then
    BLUEGREEN_REASON=""
    if nginx_ready_for_bluegreen; then
      USE_BLUEGREEN=1
    else
      log "blue/green unavailable, falling back to recreate (brief outage): $BLUEGREEN_REASON"
      # Recreate only ever drives the blue services.
      if [ "$HEALTH_URL_DERIVED" = 1 ]; then HEALTH_URL="http://127.0.0.1:$BLUE_BACKEND_PORT"; fi
    fi
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

  if [ "$USE_BLUEGREEN" = 1 ]; then
    deploy_bluegreen
    if [ "$IMAGE_SOURCE" = registry ]; then prune_images; fi
    log "updated to $TARGET_COMMIT (serving from the $ACTIVE pair)"
    finish updated ""
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
