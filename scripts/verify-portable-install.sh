#!/bin/sh
set -eu

[ "$#" -eq 2 ] && [ "$1" = "--prefix" ] || {
  echo "usage: $0 --prefix /absolute/install/path" >&2
  exit 2
}
prefix=$2
case "$prefix" in /*) ;; *) echo "error: --prefix must be absolute" >&2; exit 2;; esac
[ -d "$prefix" ] || { echo "error: prefix does not exist" >&2; exit 1; }
prefix=$(CDPATH= cd -- "$prefix" && pwd -P)

current="$prefix/current"
[ -L "$current" ] || { echo "error: current is not a symbolic link" >&2; exit 1; }
release_dir=$(CDPATH= cd -- "$current" && pwd -P)
case "$release_dir" in "$prefix"/releases/*) ;; *) echo "error: current points outside releases" >&2; exit 1;; esac
manifest="$release_dir/INSTALL-MANIFEST.txt"
[ -f "$manifest" ] || { echo "error: manifest missing" >&2; exit 1; }
binary="$release_dir/bin/wecom-mcp-v2"
config="$release_dir/config/zoop_wecom_zhycit.json.example"
[ -x "$binary" ] || { echo "error: binary missing or not executable" >&2; exit 1; }
[ -f "$config" ] || { echo "error: config example missing" >&2; exit 1; }

expected_binary=$(sed -n 's/^binary_sha256=//p' "$manifest")
expected_config=$(sed -n 's/^config_example_sha256=//p' "$manifest")
[ -n "$expected_binary" ] && [ -n "$expected_config" ] || { echo "error: manifest checksums missing" >&2; exit 1; }
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
actual_binary=$(sha256 "$binary")
actual_config=$(sha256 "$config")
[ "$actual_binary" = "$expected_binary" ] || { echo "error: binary checksum mismatch" >&2; exit 1; }
[ "$actual_config" = "$expected_config" ] || { echo "error: config checksum mismatch" >&2; exit 1; }

echo "verification=passed"
echo "release_dir=$release_dir"
sed -n '/^version=/p;/^source_commit=/p;/^source_tree=/p;/^candidate_dirty=/p' "$manifest"
