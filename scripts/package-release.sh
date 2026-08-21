#!/bin/sh
# Produce deterministic-named GitHub Release assets. Publishing is deliberately
# outside this script: it only writes local artifacts and SHA-256 manifests.
set -eu

usage() { echo "usage: $0 --version vX.Y.Z --output /absolute/empty/output-dir" >&2; exit 2; }
[ "$#" -eq 4 ] || usage
[ "$1" = "--version" ] && [ "$3" = "--output" ] || usage
version=$2
output=$4
case "$version" in v[0-9]*) ;; *) echo "error: version must be a fixed release label such as v1.2.3" >&2; exit 2;; esac
case "$version" in *[!A-Za-z0-9._-]*) echo "error: version contains unsupported characters" >&2; exit 2;; esac
case "$output" in /*) ;; *) echo "error: --output must be absolute" >&2; exit 2;; esac
[ ! -e "$output" ] || { echo "error: output already exists" >&2; exit 1; }

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH= cd -- "$script_dir/.." && pwd)
[ -z "$(git -C "$repo_dir" status --porcelain=v1 --untracked-files=normal)" ] || {
  echo "error: release packaging requires a clean Git checkout" >&2
  exit 1
}
mkdir -p "$output"
work=$(mktemp -d "${TMPDIR:-/tmp}/wecom-mcp-package.XXXXXX")
cleanup() { rm -rf "$work"; }
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
commit=$(git -C "$repo_dir" rev-parse HEAD)
tree=$(git -C "$repo_dir" rev-parse HEAD^{tree})

for target in darwin/arm64 darwin/amd64 linux/arm64 linux/amd64; do
  goos=${target%/*}
  goarch=${target#*/}
  stage="$work/${goos}-${goarch}"
  mkdir -p "$stage/bin" "$stage/config"
  (
    cd "$repo_dir"
    GOOS="$goos" GOARCH="$goarch" go build -trimpath -o "$stage/bin/wecom-mcp-v2" ./cmd/wecom-mcp-v2
    GOOS="$goos" GOARCH="$goarch" go build -trimpath -o "$stage/bin/wecom-mcp-v2-configure" ./cmd/wecom-mcp-v2-configure
  )
  chmod 0755 "$stage/bin/wecom-mcp-v2" "$stage/bin/wecom-mcp-v2-configure"
  cp "$repo_dir/config/zoop_wecom_zhycit.json.example" "$stage/config/zoop_wecom_zhycit.json.example"
  {
    echo "format=wecom-mcp-github-release-v1"
    echo "version=$version"
    echo "platform=$goos/$goarch"
    echo "source_commit=$commit"
    echo "source_tree=$tree"
    echo "binary_sha256=$(sha256 "$stage/bin/wecom-mcp-v2")"
    echo "configure_sha256=$(sha256 "$stage/bin/wecom-mcp-v2-configure")"
    echo "config_example_sha256=$(sha256 "$stage/config/zoop_wecom_zhycit.json.example")"
  } > "$stage/INSTALL-MANIFEST.txt"
  asset="wecom-mcp-v2_${version}_${goos}_${goarch}.tar.gz"
  tar -C "$stage" -czf "$output/$asset" .
done
cp "$repo_dir/install.sh" "$output/install.sh"
chmod 0755 "$output/install.sh"
{
  echo "format=wecom-mcp-github-release-index-v1"
  echo "version=$version"
  echo "source_commit=$commit"
  echo "source_tree=$tree"
  echo "supported_core=darwin/arm64,darwin/amd64,linux/arm64,linux/amd64"
  echo "unsupported_core=windows/amd64 (current syscall.Flock implementation does not build on Windows)"
  echo "installer=install.sh"
  echo "checksums=SHA256SUMS"
  echo "asset_darwin_arm64=wecom-mcp-v2_${version}_darwin_arm64.tar.gz"
  echo "asset_darwin_amd64=wecom-mcp-v2_${version}_darwin_amd64.tar.gz"
  echo "asset_linux_arm64=wecom-mcp-v2_${version}_linux_arm64.tar.gz"
  echo "asset_linux_amd64=wecom-mcp-v2_${version}_linux_amd64.tar.gz"
} > "$output/RELEASE-MANIFEST.txt"
(
  cd "$output"
  for file in install.sh RELEASE-MANIFEST.txt wecom-mcp-v2_"$version"_*.tar.gz; do
    printf '%s  %s\n' "$(sha256 "$file")" "$file"
  done > SHA256SUMS
)
echo "release_output=$output"
