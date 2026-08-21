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
tmp_status=$(mktemp "${TMPDIR:-/tmp}/wecom-mcp-status.XXXXXX")
cleanup() { rm -f "$tmp_output" "$tmp_binary" "$tmp_status"; }
trap cleanup EXIT HUP INT TERM

sha256() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print tolower($1)}'
  elif command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print tolower($1)}'
  else
    echo "error: shasum or sha256sum is required" >&2
    exit 3
  fi
}

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
  printf '%s' "$status" > "$tmp_status"
  echo "status_sha256=$(sha256 "$tmp_status")"
  echo "go_version=$(go version)"
  cd "$repo_dir"
  run test go test ./...
  run vet go vet ./...
  run build go build -trimpath -o "$tmp_binary" ./cmd/wecom-mcp-v2
  echo "built_binary_sha256=$(sha256 "$tmp_binary")"
  echo "overall=passed"
} > "$tmp_output"
mv "$tmp_output" "$output"
trap - EXIT HUP INT TERM
rm -f "$tmp_binary"
echo "evidence=$output"
