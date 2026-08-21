#!/bin/sh
set -eu

usage() {
  echo "usage: $0 --previous-source /absolute/clean/checkout --prefix /absolute/new/install/path --output /absolute/evidence.txt" >&2
  exit 2
}

[ "$#" -eq 6 ] || usage
[ "$1" = "--previous-source" ] || usage
previous_source=$2
[ "$3" = "--prefix" ] || usage
prefix=$4
[ "$5" = "--output" ] || usage
output=$6

case "$previous_source:$prefix:$output" in
  /*:/*:/*) ;;
  *) echo "error: all paths must be absolute" >&2; exit 2 ;;
esac

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
source_dir=$(CDPATH= cd -- "$script_dir/.." && pwd)
[ -d "$previous_source/.git" ] || git -C "$previous_source" rev-parse --git-dir >/dev/null 2>&1 || {
  echo "error: previous source is not a Git checkout" >&2
  exit 2
}
[ ! -e "$prefix" ] || { echo "error: prefix must not exist" >&2; exit 2; }
[ ! -e "$output" ] || { echo "error: output must not exist" >&2; exit 2; }
[ -z "$(git -C "$source_dir" status --porcelain=v1 --untracked-files=normal)" ] || {
  echo "error: current source checkout is not clean" >&2
  exit 1
}
[ -z "$(git -C "$previous_source" status --porcelain=v1 --untracked-files=normal)" ] || {
  echo "error: previous source checkout is not clean" >&2
  exit 1
}

mkdir -p "$(dirname -- "$output")"
tmp_output="$output.tmp.$$"
tmp_binary=$(mktemp "${TMPDIR:-/tmp}/wecom-mcp-lifecycle-build.XXXXXX")
go_cache=$(mktemp -d "${TMPDIR:-/tmp}/wecom-mcp-lifecycle-gocache.XXXXXX")
export GOCACHE="$go_cache"
cleanup() { rm -f "$tmp_output" "$tmp_binary"; rm -rf "$go_cache"; }
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
  echo "started_at_utc=$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  "$@" 2>&1
  echo "ended_at_utc=$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  echo "result_$label=passed"
}

expect_rejection() {
  label=$1
  shift
  echo "command_expected_rejection=$*"
  echo "started_at_utc=$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  set +e
  "$@" > "$prefix/.expected-rejection-output" 2>&1
  status=$?
  set -e
  sed 's/^/rejection_output=/' "$prefix/.expected-rejection-output"
  rm -f "$prefix/.expected-rejection-output"
  [ "$status" -ne 0 ] || { echo "error: command unexpectedly succeeded"; return 1; }
  echo "rejection_exit_status=$status"
  echo "ended_at_utc=$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  echo "result_$label=passed"
}

{
  echo "format=wecom-mcp-portable-lifecycle-v1"
  echo "captured_at_utc=$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
  echo "source_commit=$(git -C "$source_dir" rev-parse HEAD)"
  echo "source_tree=$(git -C "$source_dir" rev-parse HEAD^{tree})"
  echo "source_checkout_clean=yes"
  echo "source_detached=$(if git -C "$source_dir" symbolic-ref -q HEAD >/dev/null; then echo no; else echo yes; fi)"
  echo "source_tree_listing_begin"
  git -C "$source_dir" ls-tree -r --full-tree HEAD
  echo "source_tree_listing_end"
  echo "previous_commit=$(git -C "$previous_source" rev-parse HEAD)"
  echo "previous_tree=$(git -C "$previous_source" rev-parse HEAD^{tree})"
  echo "previous_checkout_clean=yes"
  echo "go_version=$(go version)"

  cd "$source_dir"
  run test go test ./...
  run vet go vet ./...
  run build go build -trimpath -o "$tmp_binary" ./cmd/wecom-mcp-v2
  echo "built_binary_sha256=$(sha256 "$tmp_binary")"

  run install_previous "$source_dir/scripts/install-portable.sh" --prefix "$prefix" --source "$previous_source"
  previous_version=$(sed -n 's/^version=//p' "$prefix/current/INSTALL-MANIFEST.txt")
  echo "previous_manifest_begin"
  sed 's/^/previous_manifest=/' "$prefix/current/INSTALL-MANIFEST.txt"
  echo "previous_manifest_end"
  run verify_previous "$source_dir/scripts/verify-portable-install.sh" --prefix "$prefix"

  sleep 1
  run install_current "$source_dir/scripts/install-portable.sh" --prefix "$prefix"
  current_version=$(sed -n 's/^version=//p' "$prefix/current/INSTALL-MANIFEST.txt")
  [ "$previous_version" != "$current_version" ] || { echo "error: versions are identical"; exit 1; }
  echo "current_link_after_install=$(readlink "$prefix/current")"
  echo "current_manifest_begin"
  sed 's/^/current_manifest=/' "$prefix/current/INSTALL-MANIFEST.txt"
  echo "current_manifest_end"
  run verify_current "$source_dir/scripts/verify-portable-install.sh" --prefix "$prefix"

  current_config="$prefix/current/config/zoop_wecom_zhycit.json.example"
  cp "$current_config" "$prefix/.config-pristine"
  printf '\nTAMPER-REJECTION-PROBE\n' >> "$current_config"
  expect_rejection tamper_rejected "$source_dir/scripts/verify-portable-install.sh" --prefix "$prefix"
  mv "$prefix/.config-pristine" "$current_config"
  run verify_after_restore "$source_dir/scripts/verify-portable-install.sh" --prefix "$prefix"

  run rollback_previous "$source_dir/scripts/rollback-portable.sh" --prefix "$prefix" --version "$previous_version"
  echo "current_link_after_rollback=$(readlink "$prefix/current")"
  run verify_after_rollback "$source_dir/scripts/verify-portable-install.sh" --prefix "$prefix"

  run switch_current "$source_dir/scripts/rollback-portable.sh" --prefix "$prefix" --version "$current_version"
  echo "current_link_after_switch=$(readlink "$prefix/current")"
  run verify_after_switch "$source_dir/scripts/verify-portable-install.sh" --prefix "$prefix"
  echo "overall=passed"
} > "$tmp_output"

mv "$tmp_output" "$output"
trap - EXIT HUP INT TERM
rm -f "$tmp_binary"
rm -rf "$go_cache"
echo "evidence=$output"
