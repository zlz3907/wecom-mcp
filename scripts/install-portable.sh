#!/bin/sh
set -eu

usage() {
  echo "usage: $0 --prefix /absolute/install/path [--source /absolute/clean/source]" >&2
  exit 2
}

[ "$#" -eq 2 ] || [ "$#" -eq 4 ] || usage
[ "$1" = "--prefix" ] || usage
prefix=$2
source_override=
if [ "$#" -eq 4 ]; then
  [ "$3" = "--source" ] || usage
  source_override=$4
  case "$source_override" in /*) ;; *) echo "error: --source must be absolute" >&2; exit 2;; esac
fi
case "$prefix" in
  /*) ;;
  *) echo "error: --prefix must be absolute" >&2; exit 2 ;;
esac
mkdir -p "$prefix"
prefix=$(CDPATH= cd -- "$prefix" && pwd -P)

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH= cd -- "$script_dir/.." && pwd)
if [ -n "$source_override" ]; then
  repo_dir=$(CDPATH= cd -- "$source_override" && pwd -P)
  git -C "$repo_dir" rev-parse --git-dir >/dev/null 2>&1 || {
    echo "error: --source is not a Git checkout" >&2
    exit 2
  }
fi
commit=$(git -C "$repo_dir" rev-parse HEAD)
tree=$(git -C "$repo_dir" rev-parse HEAD^{tree})
short_commit=$(printf '%s' "$commit" | cut -c1-12)
dirty=no
if [ -n "$(git -C "$repo_dir" status --porcelain --untracked-files=normal)" ]; then
  dirty=yes
  version="candidate-$(date -u '+%Y%m%dT%H%M%SZ')-tree-$(printf '%s' "$tree" | cut -c1-12)"
else
  version="git-$short_commit"
fi

release_dir="$prefix/releases/$version"
[ ! -e "$release_dir" ] || { echo "error: release already exists: $release_dir" >&2; exit 1; }
mkdir -p "$release_dir/bin" "$release_dir/config"

tmp_binary="$release_dir/bin/.wecom-mcp-v2.tmp"
candidate_input=$(mktemp "${TMPDIR:-/tmp}/wecom-mcp-candidate.XXXXXX")
cleanup() { rm -f "$tmp_binary" "$candidate_input"; }
trap cleanup EXIT HUP INT TERM
(
  cd "$repo_dir"
  go build -trimpath -o "$tmp_binary" ./cmd/wecom-mcp-v2
)
chmod 0755 "$tmp_binary"
mv "$tmp_binary" "$release_dir/bin/wecom-mcp-v2"
cp "$repo_dir/config/zoop_wecom_zhycit.json.example" "$release_dir/config/zoop_wecom_zhycit.json.example"

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

binary_sha=$(sha256 "$release_dir/bin/wecom-mcp-v2")
config_sha=$(sha256 "$release_dir/config/zoop_wecom_zhycit.json.example")
git -C "$repo_dir" diff --binary HEAD -- . > "$candidate_input"
git -C "$repo_dir" ls-files --others --exclude-standard | while IFS= read -r file; do
  printf 'untracked=%s\n' "$file"
  sha256 "$repo_dir/$file"
done >> "$candidate_input"
candidate_sha=$(sha256 "$candidate_input")
{
  echo "format=wecom-mcp-portable-v1"
  echo "version=$version"
  echo "source_commit=$commit"
  echo "source_tree=$tree"
  echo "candidate_dirty=$dirty"
  echo "candidate_content_sha256=$candidate_sha"
  echo "binary_sha256=$binary_sha"
  echo "config_example_sha256=$config_sha"
  echo "installed_at_utc=$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
} > "$release_dir/INSTALL-MANIFEST.txt"

current="$prefix/current"
if [ -e "$current" ] && [ ! -L "$current" ]; then
  echo "error: current exists but is not a symbolic link" >&2
  exit 3
fi
temporary_current="$prefix/.current-$version-$$"
rm -f "$temporary_current"
ln -s "releases/$version" "$temporary_current"
mv -f "$temporary_current" "$current"
trap - EXIT HUP INT TERM
echo "installed_version=$version"
echo "installed_path=$release_dir"
