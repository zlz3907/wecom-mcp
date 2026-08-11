#!/bin/sh
set -eu

[ "$#" -eq 2 ] && [ "$1" = "--output" ] || {
  echo "usage: $0 --output /absolute/evidence.txt" >&2
  exit 2
}
output=$2
case "$output" in /*) ;; *) echo "error: --output must be absolute" >&2; exit 2;; esac

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH= cd -- "$script_dir/.." && pwd)
tmp_output="$output.tmp.$$"
tmp_binary=$(mktemp "${TMPDIR:-/tmp}/wecom-mcp-build.XXXXXX")
cleanup() { rm -f "$tmp_output" "$tmp_binary"; }
trap cleanup EXIT HUP INT TERM

run() {
  label=$1
  shift
  echo "command=$*"
  started=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
  echo "started_at_utc=$started"
  if "$@" 2>&1; then result=passed; else result=failed; fi
  ended=$(date -u '+%Y-%m-%dT%H:%M:%SZ')
  echo "ended_at_utc=$ended"
  echo "result_$label=$result"
  [ "$result" = passed ]
}

mkdir -p "$(dirname -- "$output")"
{
  echo "format=wecom-mcp-verification-v1"
  echo "captured_at_utc=$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  echo "source_commit=$(git -C "$repo_dir" rev-parse HEAD)"
  echo "source_tree=$(git -C "$repo_dir" rev-parse HEAD^{tree})"
  status=$(git -C "$repo_dir" status --porcelain=v1 --untracked-files=normal)
  if [ -n "$status" ]; then echo "candidate_dirty=yes"; else echo "candidate_dirty=no"; fi
  echo "status_sha256=$(printf '%s' "$status" | shasum -a 256 | awk '{print $1}')"
  echo "go_version=$(go version)"
  cd "$repo_dir"
  run test go test ./...
  run vet go vet ./...
  run build go build -trimpath -o "$tmp_binary" ./cmd/wecom-mcp-v2
  echo "built_binary_sha256=$(shasum -a 256 "$tmp_binary" | awk '{print $1}')"
  echo "overall=passed"
} > "$tmp_output"
mv "$tmp_output" "$output"
trap - EXIT HUP INT TERM
rm -f "$tmp_binary"
echo "evidence=$output"
