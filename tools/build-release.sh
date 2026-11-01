#!/usr/bin/env bash
# Build only a committed, clean source snapshot; never bundle machine state.
set -euo pipefail
cd "$(dirname "$0")/.."
version=${1:-v$(tr -d '[:space:]' < VERSION)}
output=${2:-dist/release}
[[ "$version" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || { echo 'Expected vX.Y.Z[-suffix]' >&2; exit 1; }
[[ "$version" == "v$(tr -d '[:space:]' < VERSION)" ]] || { echo 'VERSION must exactly match the release version' >&2; exit 1; }
[[ -z "$(git status --porcelain)" ]] || { echo 'Release requires a clean committed worktree' >&2; exit 1; }
commit=$(git rev-parse HEAD)
build_date=$(git show -s --format=%cI "$commit")
epoch=$(git show -s --format=%ct "$commit")
mkdir -p "$output"; output=$(cd "$output" && pwd)
[[ -z "$(find "$output" -mindepth 1 -maxdepth 1 -print -quit)" ]] || { echo 'Use an empty release directory' >&2; exit 1; }
stage=$(mktemp -d)
trap 'rm -rf "$stage"' EXIT
mkdir "$stage/source" "$stage/common"
git archive "$commit" | tar -xf - -C "$stage/source"
cd "$stage/source"
python3 tools/release-notices.py --inventory "$output/go-dependencies.json" --notices "$stage/common/THIRD_PARTY_LICENSES"
for path in LICENSE NOTICE.md README.md SECURITY.md CONTRIBUTING.md CHANGELOG.md VERSION docs protocol skills deploy; do
  cp -R "$path" "$stage/common/"
done
mkdir "$stage/common/tools" "$stage/common/scripts"
cp tools/init-config.py tools/smoke-installed.py "$stage/common/tools/"
cp scripts/recovery_smoke.py "$stage/common/scripts/"
printf 'version=%s\ncommit=%s\nbuild_date=%s\ndirty=false\nCGO_ENABLED=0\narm64_execution=not-tested\n' "$version" "$commit" "$build_date" > "$stage/common/BUILD-INFO.txt"
cp "$output/go-dependencies.json" "$stage/common/"
for arch in amd64 arm64; do
  name="task-ledger-demo_${version}_linux_${arch}"
  mkdir -p "$stage/$name/bin"
  cp -R "$stage/common/." "$stage/$name/"
  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -buildvcs=false -trimpath \
    -ldflags "-s -w -X main.version=$version -X main.commit=$commit -X main.buildDate=$build_date" \
    -o "$stage/$name/bin/taskledgerd" ./cmd/taskledgerd
  tar --sort=name --mtime="@$epoch" --owner=0 --group=0 --numeric-owner -cf - -C "$stage" "$name" | gzip -n > "$output/$name.tar.gz"
done
(cd "$output" && sha256sum ./*.tar.gz go-dependencies.json > SHA256SUMS)
printf 'Built %s from %s. Linux arm64 is cross-compiled only.\n' "$version" "$commit"
