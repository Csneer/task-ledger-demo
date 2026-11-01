#!/usr/bin/env bash
# Extract away from the source tree and exercise the shipped installation.
set -euo pipefail
cd "$(dirname "$0")/.."
output=$(cd "${1:-dist/release}" && pwd)
version=${2:-v$(tr -d '[:space:]' < VERSION)}
[[ "$(uname -s):$(uname -m)" == Linux:x86_64 ]] || { echo 'Execution smoke requires Linux amd64' >&2; exit 1; }
(cd "$output" && sha256sum --check SHA256SUMS)
scratch=$(mktemp -d)
child_pid=
cleanup() {
  status=$?
  trap - EXIT INT TERM
  if [[ -n "$child_pid" ]]; then
    if kill -0 "$child_pid" 2>/dev/null; then kill -TERM "$child_pid"; fi
    if ! wait "$child_pid"; then if [[ "$status" == 0 ]]; then status=1; fi; fi
  fi
  if [[ "$status" == 0 ]]; then rm -rf "$scratch"; else printf 'Smoke failed; private evidence retained at %s\n' "$scratch" >&2; fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
run_child() {
  "$@" & child_pid=$!
  status=0; wait "$child_pid" || status=$?
  child_pid=
  return "$status"
}
tar -xzf "$output/task-ledger-demo_${version}_linux_amd64.tar.gz" -C "$scratch"
root="$scratch/task-ledger-demo_${version}_linux_amd64"
[[ "$("$root/bin/taskledgerd" -version)" == "$version ("* ]] || { echo 'Version metadata mismatch' >&2; exit 1; }
test -f "$root/THIRD_PARTY_LICENSES/index.json"
test -f "$root/skills/task-ledger/SKILL.md"
test -f "$root/deploy/task-ledger.service"
run_child python3 "$root/tools/smoke-installed.py" --release "$root" --dir "$scratch/instance"
run_child python3 "$root/scripts/recovery_smoke.py" --daemon "$root/bin/taskledgerd" --dir "$scratch/recovery"
echo 'PASS extracted Linux amd64 release. Linux arm64 was cross-compiled only.'
