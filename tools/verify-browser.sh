#!/usr/bin/env bash
# Opt-in synthetic browser checks. Never points at an existing live instance.
set -euo pipefail
cd "$(dirname "$0")/.."
evidence=${1:-runtime/validation/browser}
mkdir -p "$(dirname "$evidence")"
[[ ! -e "$evidence" ]] || { echo 'Browser evidence directory must be new' >&2; exit 1; }
mkdir -m 700 "$evidence"
parent=$(mktemp -d)
fixture_pid=
test_pid=
stage=prepare
fixture_exit_ok=true
cleanup() {
  status=$?
  trap - EXIT INT TERM
  if [[ -n "$test_pid" ]]; then
    if kill -0 "$test_pid" 2>/dev/null; then kill -TERM "$test_pid"; fi
    if ! wait "$test_pid"; then if [[ "$status" == 0 ]]; then status=1; fi; fi
  fi
  if [[ -n "$fixture_pid" ]]; then
    if kill -0 "$fixture_pid" 2>/dev/null; then kill -TERM "$fixture_pid"; fi
    if ! wait "$fixture_pid"; then fixture_exit_ok=false; if [[ "$status" == 0 ]]; then status=1; fi; fi
  fi
  # Retain any completed synthetic checks even when a later check fails.
  for name in browser-results.json usability-results.json; do
    if [[ -f "$parent/instance/$name" ]]; then cp "$parent/instance/$name" "$evidence/"; fi
  done
  if [[ -d "$parent/instance" ]]; then find "$parent/instance" -maxdepth 1 -name '*.png' -exec cp {} "$evidence/" \;; fi
  printf '{"exit_code":%s,"last_stage":"%s","fixture_exit_ok":%s}\n' "$status" "$stage" "$fixture_exit_ok" > "$evidence/run-summary.json"
  if [[ "$status" == 0 ]]; then rm -rf "$parent"; else printf 'Browser checks failed; private fixture retained at %s\n' "$parent" >&2; fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
run_test() {
  "$@" & test_pid=$!
  status=0; wait "$test_pid" || status=$?
  test_pid=
  return "$status"
}
stage=build
go build -o "$parent/taskledgerd" ./cmd/taskledgerd
stage=fixture
node scripts/frontend_fixture.mjs --daemon "$parent/taskledgerd" --dir "$parent/instance" > "$parent/fixture.log" 2>&1 &
fixture_pid=$!
for _ in $(seq 1 100); do
  [[ ! -f "$parent/instance/fixture.json" ]] || break
  kill -0 "$fixture_pid" 2>/dev/null || { echo 'Fixture exited; no browser checks executed' >&2; exit 1; }
  sleep .1
done
test -f "$parent/instance/fixture.json"
stage=flow
run_test node scripts/frontend_e2e.mjs "$parent/instance"
stage=seed
run_test node scripts/usability_fixture.mjs "$parent/instance"
stage=usability
run_test node scripts/usability_e2e.mjs "$parent/instance"
stage=complete
echo "PASS browser flow and usability; evidence: $evidence"
