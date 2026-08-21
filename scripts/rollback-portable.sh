#!/bin/sh
set -eu

[ "$#" -eq 4 ] && [ "$1" = "--prefix" ] && [ "$3" = "--version" ] || {
  echo "usage: $0 --prefix /absolute/install/path --version VERSION" >&2
  exit 2
}
prefix=$2
version=$4
case "$prefix" in /*) ;; *) echo "error: --prefix must be absolute" >&2; exit 2;; esac
case "$version" in *[!A-Za-z0-9._-]*|'') echo "error: invalid version" >&2; exit 2;; esac
[ -d "$prefix" ] || { echo "error: prefix does not exist" >&2; exit 1; }
prefix=$(CDPATH= cd -- "$prefix" && pwd -P)

target="$prefix/releases/$version"
manifest="$target/INSTALL-MANIFEST.txt"
binary="$target/bin/wecom-mcp-v2"
config="$target/config/zoop_wecom_zhycit.json.example"
[ -x "$binary" ] || { echo "error: rollback binary missing" >&2; exit 1; }
[ -f "$manifest" ] || { echo "error: rollback manifest missing" >&2; exit 1; }
[ -f "$config" ] || { echo "error: rollback config example missing" >&2; exit 1; }
expected_binary=$(sed -n 's/^binary_sha256=//p' "$manifest")
expected_config=$(sed -n 's/^config_example_sha256=//p' "$manifest")
[ "$(printf '%s' "$expected_binary" | awk 'length == 64 && $0 !~ /[^0-9a-f]/ { print "ok" }')" = ok ] || { echo "error: rollback binary manifest hash is invalid" >&2; exit 1; }
[ "$(printf '%s' "$expected_config" | awk 'length == 64 && $0 !~ /[^0-9a-f]/ { print "ok" }')" = ok ] || { echo "error: rollback config manifest hash is invalid" >&2; exit 1; }
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
[ "$(sha256 "$binary")" = "$expected_binary" ] || { echo "error: rollback binary checksum mismatch; current was retained" >&2; exit 1; }
[ "$(sha256 "$config")" = "$expected_config" ] || { echo "error: rollback config checksum mismatch; current was retained" >&2; exit 1; }
current="$prefix/current"
if [ -e "$current" ] && [ ! -L "$current" ]; then
  echo "error: current exists but is not a symbolic link; current was retained" >&2
  exit 3
fi
temporary_current="$prefix/.current-rollback-$version-$$"
rm -f "$temporary_current"
ln -s "releases/$version" "$temporary_current"
mv -f "$temporary_current" "$current"
echo "rolled_back_to=$version"
