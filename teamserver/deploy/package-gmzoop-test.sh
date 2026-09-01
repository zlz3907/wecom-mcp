#!/bin/sh
# Builds a locally transferable Linux amd64 gmzoop test package. It never
# includes instance configuration, Schema, or secrets.
set -eu

usage() { echo "usage: $0 --output /absolute/empty/output-dir" >&2; exit 2; }
[ "$#" -eq 2 ] || usage
[ "$1" = "--output" ] || usage
output=$2
case "$output" in /*) ;; *) usage ;; esac
[ ! -e "$output" ] || { echo "error: output already exists" >&2; exit 1; }

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH= cd -- "$script_dir/../.." && pwd)
[ -z "$(git -C "$repo_dir" status --porcelain=v1 --untracked-files=normal)" ] || {
  echo "error: package requires a clean Git checkout" >&2
  exit 1
}
revision=$(git -C "$repo_dir" rev-parse --short=12 HEAD)
work=$(mktemp -d "${TMPDIR:-/tmp}/wecom-team-package.XXXXXX")
cleanup() { rm -rf "$work"; }
trap cleanup EXIT HUP INT TERM
stage="$work/wecom-mcp-team-gmzoop-$revision"
mkdir -p "$stage"

(
  cd "$repo_dir/teamserver"
  CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "$stage/wecom-mcp-team" ./cmd/wecom-mcp-team
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "$stage/wecom-mcp-instance-init" ./cmd/wecom-mcp-instance-init
)
chmod 0755 "$stage/wecom-mcp-team"
chmod 0755 "$stage/wecom-mcp-instance-init"
cp "$repo_dir/teamserver/deploy/gmzoop.env.example" "$stage/gmzoop.env.example"
cp "$repo_dir/teamserver/deploy/create-gmzoop-env.sh" "$stage/create-gmzoop-env.sh"
cp "$repo_dir/teamserver/deploy/INSTALL-GMZOOP-TEST.md" "$stage/INSTALL-GMZOOP-TEST.md"
cp "$repo_dir/teamserver/deploy/wecom-mcp@.service.example" "$stage/wecom-mcp@.service"
cp "$repo_dir/evidence/server-preflight/team-mcp-test-deployment-package/nginx-mcp.jyiai.com-gmzoop.conf" "$stage/nginx-mcp.jyiai.com-gmzoop.conf"
cp "$repo_dir/teamserver/WECHAT-SUBJECT-BINDING.md" "$stage/WECHAT-SUBJECT-BINDING.md"
cp "$repo_dir/LICENSE" "$stage/LICENSE"
chmod 0755 "$stage/create-gmzoop-env.sh"

sha256() {
  if command -v shasum >/dev/null 2>&1; then shasum -a 256 "$1" | awk '{print tolower($1)}'; else sha256sum "$1" | awk '{print tolower($1)}'; fi
}
{
  echo "format=wecom-mcp-team-gmzoop-test-v1"
  echo "source_commit=$(git -C "$repo_dir" rev-parse HEAD)"
  echo "platform=linux/amd64"
  echo "authentication_mode=connector_api_key_configured_role"
  echo "server_binary_sha256=$(sha256 "$stage/wecom-mcp-team")"
  echo "initializer_binary_sha256=$(sha256 "$stage/wecom-mcp-instance-init")"
} > "$stage/DEPLOY-MANIFEST.txt"
(
  cd "$stage"
  for file in DEPLOY-MANIFEST.txt INSTALL-GMZOOP-TEST.md LICENSE WECHAT-SUBJECT-BINDING.md create-gmzoop-env.sh gmzoop.env.example nginx-mcp.jyiai.com-gmzoop.conf wecom-mcp-instance-init wecom-mcp-team wecom-mcp@.service; do
    printf '%s  %s\n' "$(sha256 "$file")" "$file"
  done > SHA256SUMS
)
mkdir -p "$output"
archive="$output/wecom-mcp-team_gmzoop-test_${revision}_linux_amd64.tar.gz"
# macOS tar otherwise writes AppleDouble `._*` entries for filesystem metadata.
# They are not deployment assets and produce noisy Linux extraction warnings.
COPYFILE_DISABLE=1 tar -C "$work" -czf "$archive" "$(basename "$stage")"
printf 'package=%s\nsha256=%s\n' "$archive" "$(sha256 "$archive")"
