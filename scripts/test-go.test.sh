#!/usr/bin/env bash
set -eu

SCRIPT_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
TEST_DIR=$(mktemp -d "${TMPDIR:-/tmp}/multica-test-go.XXXXXX")
BIN_DIR="$TEST_DIR/bin"
CALLS_FILE="$TEST_DIR/go-calls.log"
PSQL_CALLS_FILE="$TEST_DIR/psql-calls.log"
COMMITS_FILE="$TEST_DIR/xact-commit"
OUTPUT_FILE="$TEST_DIR/output.log"
RUN_DB_FILE="$TEST_DIR/run-db.log"
ENV_FILE_LOG="$TEST_DIR/go-env.log"

cleanup() {
  rm -rf "$TEST_DIR"
}
trap cleanup EXIT

mkdir -p "$BIN_DIR"

# The runner reads the checkout's env file when nothing is exported, and a
# developer's .env names a per-worktree database. Pin the input so the run
# database's name is predictable wherever this test runs.
export DATABASE_URL='postgres://multica:multica@localhost:5432/multica?sslmode=disable'
unset TEST_DATABASE_URL MULTICA_REQUIRE_TEST_DB MULTICA_TEST_DB_ACTIVE MULTICA_TEST_DB_EXPECT_USE
export MULTICA_TEST_GO_CALLS="$CALLS_FILE"
export MULTICA_TEST_PSQL_CALLS="$PSQL_CALLS_FILE"
export MULTICA_TEST_PSQL_COMMITS="$COMMITS_FILE"
export MULTICA_TEST_RUN_DB="$RUN_DB_FILE"
export MULTICA_TEST_GO_ENV="$ENV_FILE_LOG"
: >"$CALLS_FILE"
: >"$PSQL_CALLS_FILE"
printf '0' >"$COMMITS_FILE"
: >"$RUN_DB_FILE"
: >"$ENV_FILE_LOG"

cat >"$BIN_DIR/go" <<'FAKE'
#!/usr/bin/env bash
set -eu

case "${1:-}" in
  list)
    if [ "$#" -ne 2 ] || [ "$2" != "./..." ]; then
      echo "unexpected go list arguments: $*" >&2
      exit 2
    fi
    printf '%s\n' \
      github.com/multica-ai/multica/server \
      github.com/multica-ai/multica/server/internal/daemon \
      github.com/multica-ai/multica/server/pkg/agent \
      github.com/multica-ai/multica/server/pkg/agent/internal/testutil
    ;;
  test)
    printf '%s\n' "$*" >>"$MULTICA_TEST_GO_CALLS"
    # What the suite sees: the promise internal/testutil enforces, and the
    # database it was pointed at.
    printf 'MULTICA_REQUIRE_TEST_DB=%s DATABASE_URL=%s\n' \
      "${MULTICA_REQUIRE_TEST_DB:-unset}" "${DATABASE_URL:-unset}" >>"$MULTICA_TEST_GO_ENV"
    ;;
  run)
    # `go run ./cmd/migrate up` against the run's private database.
    printf '%s\n' "$*" >>"$MULTICA_TEST_RUN_DB"
    ;;
  *)
    echo "unexpected go command: $*" >&2
    exit 2
    ;;
esac
FAKE
chmod 755 "$BIN_DIR/go"

# The runner provisions a database with psql, so the suite's contract now
# includes it. The counter is what pg_stat_database.xact_commit looks like for a
# run whose tests actually connected: it moves between the runner's two reads.
cat >"$BIN_DIR/psql" <<'FAKE'
#!/usr/bin/env bash
set -eu
printf '%s\n' "$*" >>"$MULTICA_TEST_PSQL_CALLS"

case "$*" in
  *xact_commit*)
    commits=$(cat "$MULTICA_TEST_PSQL_COMMITS")
    # MULTICA_TEST_PSQL_FROZEN models a suite that never connected: the
    # counter reads the same before and after.
    if [ "${MULTICA_TEST_PSQL_FROZEN:-0}" != "1" ]; then
      commits=$((commits + 1))
    fi
    printf '%s' "$commits" >"$MULTICA_TEST_PSQL_COMMITS"
    printf '%s\n' "$commits"
    ;;
esac
exit 0
FAKE
chmod 755 "$BIN_DIR/psql"

regular_call='test -race github.com/multica-ai/multica/server github.com/multica-ai/multica/server/internal/daemon'
agent_call='test -race -p 2 -parallel 2 ./pkg/agent/...'

# $1: case label; $2: expected go calls, one per line. Clears the log after.
expect_calls() {
  actual_calls=$(cat "$CALLS_FILE")
  if [ "$actual_calls" != "$2" ]; then
    echo "$1: unexpected go test calls:" >&2
    printf '%s\n' "$actual_calls" >&2
    exit 1
  fi
  : >"$CALLS_FILE"
}

# The run is wrapped in a private database: one created and one dropped per
# invocation, and the migration applied to the database that was created rather
# than to DATABASE_URL.
expect_provisioned_database() {
  label=$1
  if ! grep -q 'CREATE DATABASE' "$PSQL_CALLS_FILE"; then
    echo "$label did not create a run database:" >&2
    cat "$PSQL_CALLS_FILE" >&2
    exit 1
  fi
  if ! grep -q 'DROP DATABASE' "$PSQL_CALLS_FILE"; then
    echo "$label did not drop the database it created:" >&2
    cat "$PSQL_CALLS_FILE" >&2
    exit 1
  fi
  if ! grep -q 'cmd/migrate up' "$RUN_DB_FILE"; then
    echo "$label did not migrate the run database:" >&2
    cat "$RUN_DB_FILE" >&2
    exit 1
  fi
  : >"$PSQL_CALLS_FILE"
  : >"$RUN_DB_FILE"
}

# Every `go test` that can open a database must run under the promise: with
# MULTICA_REQUIRE_TEST_DB=1 a DB-backed test that cannot reach its database
# fails instead of skipping (internal/testutil). $2 is the database the suite
# must have been pointed at.
expect_required_database() {
  label=$1
  if ! grep -q "^MULTICA_REQUIRE_TEST_DB=1 DATABASE_URL=$2\$" "$ENV_FILE_LOG"; then
    echo "$label ran go test without promising the database:" >&2
    cat "$ENV_FILE_LOG" >&2
    exit 1
  fi
  if grep -q 'MULTICA_REQUIRE_TEST_DB=unset' "$ENV_FILE_LOG"; then
    echo "$label ran a go test outside the promise:" >&2
    cat "$ENV_FILE_LOG" >&2
    exit 1
  fi
  : >"$ENV_FILE_LOG"
}

# pkg/agent opens no database, and the CI job that runs it has no Postgres
# service. Provisioning one there would fail the run on a server it never
# needed, so `--only agent` must reach `go test` without touching psql.
expect_no_provisioned_database() {
  label=$1
  if [ -s "$PSQL_CALLS_FILE" ] || [ -s "$RUN_DB_FILE" ]; then
    echo "$label provisioned a database for a suite that reads none:" >&2
    cat "$PSQL_CALLS_FILE" "$RUN_DB_FILE" >&2
    exit 1
  fi
}

# $1: case label; the rest are arguments for test-go.sh. Asserts the usage
# exit status, the usage line, and that go was never invoked.
expect_usage_failure() {
  label=$1
  shift
  set +e
  PATH="$BIN_DIR:$PATH" bash "$SCRIPT_DIR/test-go.sh" "$@" >"$OUTPUT_FILE" 2>&1
  status=$?
  set -e

  if [ "$status" -ne 2 ]; then
    echo "$label returned status $status, want 2" >&2
    cat "$OUTPUT_FILE" >&2
    exit 1
  fi
  if [ -s "$CALLS_FILE" ]; then
    echo "$label invoked go:" >&2
    cat "$CALLS_FILE" >&2
    exit 1
  fi
  # Usage errors must be answered before anything is provisioned: a typo must
  # not cost a database.
  if [ -s "$PSQL_CALLS_FILE" ] || [ -s "$RUN_DB_FILE" ]; then
    echo "$label provisioned a database before rejecting its arguments:" >&2
    cat "$PSQL_CALLS_FILE" "$RUN_DB_FILE" >&2
    exit 1
  fi
  if ! grep -q '^usage: .*test-go.sh \[--race\] \[--only regular|agent\]$' "$OUTPUT_FILE"; then
    echo "$label did not print usage" >&2
    cat "$OUTPUT_FILE" >&2
    exit 1
  fi
}

# The run database is named after the base with a per-run suffix.
run_db_pattern='postgres://multica:multica@localhost:5432/multica_t[0-9]*?sslmode=disable'

PATH="$BIN_DIR:$PATH" bash "$SCRIPT_DIR/test-go.sh" --race
expect_calls "--race" "$regular_call
$agent_call"
expect_provisioned_database "--race"
expect_required_database "--race" "$run_db_pattern"

# check.sh calls the wrapper with no arguments at all. It forwards its own
# argument list across the re-exec, and on bash 3.2 (the system bash on macOS)
# expanding an empty array under `set -u` aborts the script before a single
# test runs.
PATH="$BIN_DIR:$PATH" bash "$SCRIPT_DIR/test-go.sh"
expect_calls "no arguments" "${regular_call/ -race/}
${agent_call/ -race/}"
expect_provisioned_database "no arguments"

PATH="$BIN_DIR:$PATH" bash "$SCRIPT_DIR/test-go.sh" --race --only regular
expect_calls "--only regular" "$regular_call"
expect_provisioned_database "--only regular"
expect_required_database "--only regular" "$run_db_pattern"

# A database someone else provisioned (CI's Postgres service, a remote server)
# is used as-is — no create, migrate or drop — but it is the same promise: the
# suite runs under MULTICA_REQUIRE_TEST_DB=1 and must be seen connecting.
provisioned_url='postgres://ci:ci@db.example:5432/multica_ci?sslmode=disable'
TEST_DATABASE_URL="$provisioned_url" PATH="$BIN_DIR:$PATH" bash "$SCRIPT_DIR/test-go.sh" --only regular
expect_calls "TEST_DATABASE_URL" "${regular_call/ -race/}"
expect_required_database "TEST_DATABASE_URL" "$provisioned_url"
if grep -q 'CREATE DATABASE\|DROP DATABASE' "$PSQL_CALLS_FILE" || [ -s "$RUN_DB_FILE" ]; then
  echo "TEST_DATABASE_URL provisioned a database it was told already exists:" >&2
  cat "$PSQL_CALLS_FILE" "$RUN_DB_FILE" >&2
  exit 1
fi
if [ "$(grep -c 'xact_commit' "$PSQL_CALLS_FILE")" -ne 2 ]; then
  echo "TEST_DATABASE_URL did not check that the suite reached the database:" >&2
  cat "$PSQL_CALLS_FILE" >&2
  exit 1
fi
# Reading the counter through the run database moves it by itself (the
# connection's own startup transaction), so the reads must go elsewhere.
if grep 'xact_commit' "$PSQL_CALLS_FILE" | grep -q 'db.example:5432/multica_ci'; then
  echo "TEST_DATABASE_URL read the commit counter through the run database itself:" >&2
  cat "$PSQL_CALLS_FILE" >&2
  exit 1
fi
: >"$PSQL_CALLS_FILE"

# A run whose DB-backed tests never connected proved nothing and must fail,
# on both paths, even though every `go test` exited 0.
for frozen_case in provisioned as-is; do
  set +e
  if [ "$frozen_case" = as-is ]; then
    TEST_DATABASE_URL="$provisioned_url" MULTICA_TEST_PSQL_FROZEN=1 PATH="$BIN_DIR:$PATH" \
      bash "$SCRIPT_DIR/test-go.sh" --only regular >"$OUTPUT_FILE" 2>&1
  else
    MULTICA_TEST_PSQL_FROZEN=1 PATH="$BIN_DIR:$PATH" \
      bash "$SCRIPT_DIR/test-go.sh" --only regular >"$OUTPUT_FILE" 2>&1
  fi
  status=$?
  set -e
  if [ "$status" -eq 0 ]; then
    echo "$frozen_case: a run with no database use passed:" >&2
    cat "$OUTPUT_FILE" >&2
    exit 1
  fi
  if ! grep -q 'No test connected to' "$OUTPUT_FILE"; then
    echo "$frozen_case: failure did not say the suite never connected:" >&2
    cat "$OUTPUT_FILE" >&2
    exit 1
  fi
  : >"$CALLS_FILE"; : >"$PSQL_CALLS_FILE"; : >"$RUN_DB_FILE"; : >"$ENV_FILE_LOG"
done

# Option order must not matter: CI spells it one way, humans another.
PATH="$BIN_DIR:$PATH" bash "$SCRIPT_DIR/test-go.sh" --only agent --race
expect_calls "--only agent" "$agent_call"
expect_no_provisioned_database "--only agent"

expect_usage_failure "unknown option" --unknown
expect_usage_failure "unknown --only scope" --only everything
expect_usage_failure "missing --only scope" --only

echo "test-go.test.sh: PASS"
