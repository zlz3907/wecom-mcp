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
[ -x "$target/bin/wecom-mcp-v2" ] || { echo "error: rollback binary missing" >&2; exit 1; }
[ -f "$target/INSTALL-MANIFEST.txt" ] || { echo "error: rollback manifest missing" >&2; exit 1; }
ln -sfn "releases/$version" "$prefix/current"
echo "rolled_back_to=$version"
