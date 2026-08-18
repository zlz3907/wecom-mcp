#!/bin/sh
# Install a pinned wecom-mcp-v2 GitHub Release asset.  This script never
# contacts Enterprise WeCom and never creates a local credential/configuration.
set -eu

PROJECT_REPOSITORY="zlz3907/wecom-mcp"
INSTANCE_NAME="zoop_wecom_zhycit"

usage() {
  cat >&2 <<'EOF'
usage: install.sh [--version vX.Y.Z] [options]

Options:
  --client auto|codex|trae-solo-cn|workbuddy|none  Register a known client (default: auto)
  --config /absolute/path/to/zoop_wecom_zhycit.local.json
                                                   Existing local instance configuration; never copied
  --prefix /absolute/path                         Installation prefix (default: $HOME/.mcp/wecom-mcp-v2)
  --rollback VERSION                               Switch current to an already verified local release
  --uninstall                                     Remove only the current symlink; retain all releases/configuration
  --release-base URL                              Release base override for controlled mirrors/tests only
  --codex-config PATH | --trae-config PATH        Explicit known-client config paths
  --workbuddy-config PATH                         Reports blocked: WorkBuddy contract is not yet verified
EOF
  exit 2
}

version=""
client="auto"
service_config=""
prefix="${HOME:-}/.mcp/wecom-mcp-v2"
rollback=""
uninstall=no
release_base=""
codex_config=""
trae_config=""
workbuddy_config=""

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version) [ "$#" -ge 2 ] || usage; version=$2; shift 2 ;;
    --client) [ "$#" -ge 2 ] || usage; client=$2; shift 2 ;;
    --config) [ "$#" -ge 2 ] || usage; service_config=$2; shift 2 ;;
    --prefix) [ "$#" -ge 2 ] || usage; prefix=$2; shift 2 ;;
    --rollback) [ "$#" -ge 2 ] || usage; rollback=$2; shift 2 ;;
    --uninstall) uninstall=yes; shift ;;
    --release-base) [ "$#" -ge 2 ] || usage; release_base=$2; shift 2 ;;
    --codex-config) [ "$#" -ge 2 ] || usage; codex_config=$2; shift 2 ;;
    --trae-config) [ "$#" -ge 2 ] || usage; trae_config=$2; shift 2 ;;
    --workbuddy-config) [ "$#" -ge 2 ] || usage; workbuddy_config=$2; shift 2 ;;
    -h|--help) usage ;;
    *) usage ;;
  esac
done

case "$prefix" in /*) ;; *) echo "result=failed"; echo "next_action=--prefix must be absolute"; exit 2 ;; esac
case "$client" in auto|codex|trae-solo-cn|workbuddy|none) ;; *) echo "result=failed"; echo "next_action=unsupported --client"; exit 2 ;; esac
if [ -n "$version" ]; then
  case "$version" in v[0-9]*) ;; *) echo "result=failed"; echo "next_action=--version must be a fixed release version such as v1.2.3"; exit 2 ;; esac
  case "$version" in *[!A-Za-z0-9._-]*) echo "result=failed"; echo "next_action=--version contains unsupported characters"; exit 2 ;; esac
fi

sha256() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
  elif command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    echo "result=agent_blocked" >&2
    echo "next_action=install shasum or sha256sum before retrying" >&2
    exit 3
  fi
}

manifest_value() { sed -n "s/^$2=//p" "$1" | awk 'NR == 1 { print } NR == 2 { exit 1 }'; }

verify_release() {
  directory=$1
  manifest="$directory/INSTALL-MANIFEST.txt"
  binary="$directory/bin/wecom-mcp-v2"
  helper="$directory/bin/wecom-mcp-v2-configure"
  example="$directory/config/zoop_wecom_zhycit.json.example"
  [ -f "$manifest" ] && [ -x "$binary" ] && [ -x "$helper" ] && [ -f "$example" ] || return 1
  expected_binary=$(manifest_value "$manifest" binary_sha256)
  expected_helper=$(manifest_value "$manifest" configure_sha256)
  expected_example=$(manifest_value "$manifest" config_example_sha256)
  [ -n "$expected_binary" ] && [ -n "$expected_helper" ] && [ -n "$expected_example" ] || return 1
  [ "$(sha256 "$binary")" = "$expected_binary" ] || return 1
  [ "$(sha256 "$helper")" = "$expected_helper" ] || return 1
  [ "$(sha256 "$example")" = "$expected_example" ] || return 1
}

switch_current() {
  release_name=$1
  target="$prefix/releases/$release_name"
  verify_release "$target" || { echo "result=failed"; echo "next_action=rollback target failed local manifest verification"; exit 1; }
  "$target/bin/wecom-mcp-v2-configure" --switch-current "$prefix" --release "$release_name" >/dev/null || {
    echo "result=agent_blocked"; echo "next_action=unable to atomically switch current; inspect local prefix"; exit 3
  }
}

if [ "$uninstall" = yes ]; then
  [ -L "$prefix/current" ] || { echo "result=failed"; echo "next_action=no current managed release to uninstall"; exit 1; }
  current_target=$(CDPATH= cd -- "$prefix/current" && pwd -P)
  case "$current_target" in "$prefix"/releases/*) ;; *) echo "result=agent_blocked"; echo "next_action=current does not point inside managed releases"; exit 3 ;; esac
  rm "$prefix/current"
  echo "result=uninstalled"
  echo "installed=no"
  echo "configured=unchanged"
  echo "loaded=unknown"
  echo "verified=unknown"
  echo "next_action=release files and client configuration were intentionally retained; restore a client backup or remove the managed entry manually"
  exit 0
fi

if [ -n "$rollback" ]; then
  case "$rollback" in *[!A-Za-z0-9._-]*|'') echo "result=failed"; echo "next_action=invalid rollback version"; exit 2 ;; esac
  [ -d "$prefix/releases/$rollback" ] || { echo "result=failed"; echo "next_action=rollback release is not installed"; exit 1; }
  switch_current "$rollback"
  echo "result=rolled_back"
  echo "version=$rollback"
  echo "binary_path=$prefix/current/bin/wecom-mcp-v2"
  echo "rollback_target=$rollback"
  echo "installed=yes"
  echo "configured=unchanged"
  echo "loaded=no"
  echo "verified=no"
  echo "next_action=restart registered clients, then run initialize, tools/list, and a read-only wecom_schema_status call"
  exit 0
fi

case "$(uname -s)" in
  Darwin) platform_os=darwin ;;
  Linux) platform_os=linux ;;
  *) echo "result=agent_blocked"; echo "next_action=unsupported OS $(uname -s); no release was changed"; exit 3 ;;
esac
case "$(uname -m)" in
  arm64|aarch64) platform_arch=arm64 ;;
  x86_64|amd64) platform_arch=amd64 ;;
  *) echo "result=agent_blocked"; echo "next_action=unsupported architecture $(uname -m); no release was changed"; exit 3 ;;
esac

# `latest` is used only to select an immutable-looking release tag. The binary
# itself is fetched from the resulting fixed tag URL and checked against that
# release's SHA256SUMS; no binary is executed from a mutable latest endpoint.
if [ -z "$version" ]; then
  resolved_url=$(curl -fsSIL -o /dev/null -w '%{url_effective}' "https://github.com/$PROJECT_REPOSITORY/releases/latest") || {
    echo "result=agent_blocked"; echo "next_action=unable to resolve the latest GitHub Release; supply --version vX.Y.Z after checking release access"; exit 3
  }
  case "$resolved_url" in
    */releases/tag/*) version=${resolved_url##*/releases/tag/} ;;
    *) echo "result=agent_blocked"; echo "next_action=GitHub latest-release redirect did not resolve to a fixed tag"; exit 3 ;;
  esac
  case "$version" in v[0-9]*) ;; *) echo "result=agent_blocked"; echo "next_action=resolved release tag is not a supported fixed version"; exit 3 ;; esac
  case "$version" in *[!A-Za-z0-9._-]*) echo "result=agent_blocked"; echo "next_action=resolved release tag contains unsupported characters"; exit 3 ;; esac
fi

if [ -z "$release_base" ]; then
  release_base="https://github.com/$PROJECT_REPOSITORY/releases/download/$version"
fi
asset="wecom-mcp-v2_${version}_${platform_os}_${platform_arch}.tar.gz"
release_name="${version}-${platform_os}-${platform_arch}"

mkdir -p "$prefix/releases"
prefix=$(CDPATH= cd -- "$prefix" && pwd -P)
if [ -d "$prefix/releases/$release_name" ]; then
  verify_release "$prefix/releases/$release_name" || { echo "result=agent_blocked"; echo "next_action=existing release directory failed manifest verification; preserve it and inspect manually"; exit 3; }
else
  temporary_dir=$(mktemp -d "${TMPDIR:-/tmp}/wecom-mcp-install.XXXXXX")
  cleanup() { rm -rf "$temporary_dir"; }
  trap cleanup EXIT HUP INT TERM
  checksums="$temporary_dir/SHA256SUMS"
  archive="$temporary_dir/$asset"
  if ! curl -fsSL "$release_base/SHA256SUMS" -o "$checksums" || ! curl -fsSL "$release_base/$asset" -o "$archive"; then
    echo "result=agent_blocked"; echo "next_action=unable to download the fixed GitHub Release asset or checksum; confirm release visibility/version and retry"; exit 3
  fi
  expected_archive=$(awk -v file="$asset" '$2 == file { print $1 }' "$checksums")
  case "$expected_archive" in [0-9a-f][0-9a-f]*) ;; *) echo "result=agent_blocked"; echo "next_action=release checksum entry is missing or malformed"; exit 3 ;; esac
  [ "$(sha256 "$archive")" = "$expected_archive" ] || { echo "result=failed"; echo "next_action=downloaded release checksum mismatch; old current release was retained"; exit 1; }
  staging="$prefix/releases/.incoming-${release_name}-$$"
  rm -rf "$staging"
  mkdir "$staging"
  if ! tar -xzf "$archive" -C "$staging"; then
    rm -rf "$staging"
    echo "result=failed"; echo "next_action=release archive could not be extracted; old current release was retained"; exit 1
  fi
  verify_release "$staging" || { rm -rf "$staging"; echo "result=failed"; echo "next_action=release manifest/content verification failed; old current release was retained"; exit 1; }
  actual_version=$(manifest_value "$staging/INSTALL-MANIFEST.txt" version)
  actual_platform=$(manifest_value "$staging/INSTALL-MANIFEST.txt" platform)
  [ "$actual_version" = "$version" ] && [ "$actual_platform" = "$platform_os/$platform_arch" ] || { rm -rf "$staging"; echo "result=failed"; echo "next_action=release manifest does not match selected platform/version"; exit 1; }
  mv "$staging" "$prefix/releases/$release_name"
fi

previous="none"
if [ -L "$prefix/current" ]; then
  previous=$(basename "$(CDPATH= cd -- "$prefix/current" && pwd -P)")
fi
switch_current "$release_name"

configured=no
configuration_detail="no local --config supplied; binary installed but no client was registered"
if [ "$client" = none ]; then
  service_config=""
  configuration_detail="client registration explicitly skipped"
elif [ -z "$service_config" ] && [ -n "${HOME:-}" ] && [ -f "$HOME/.trae/mcp-config/wecom/zoop_wecom_zhycit.local.json" ]; then
  service_config="$HOME/.trae/mcp-config/wecom/zoop_wecom_zhycit.local.json"
  configuration_detail="using detected TRAE-compatible local instance configuration"
fi
if [ -n "$service_config" ]; then
  case "$service_config" in /*) ;; *) echo "result=agent_blocked"; echo "next_action=--config must be absolute; binary remains installed"; exit 3 ;; esac
  if [ ! -f "$service_config" ]; then
    echo "result=agent_blocked"; echo "next_action=local instance config is missing; binary remains installed and no client was registered"; exit 3
  fi
  set -- --client "$client" --binary "$prefix/current/bin/wecom-mcp-v2" --config "$service_config"
  [ -n "$codex_config" ] && set -- "$@" --codex-config "$codex_config"
  [ -n "$trae_config" ] && set -- "$@" --trae-config "$trae_config"
  [ -n "$workbuddy_config" ] && set -- "$@" --workbuddy-config "$workbuddy_config"
  set +e
  "$prefix/current/bin/wecom-mcp-v2-configure" "$@" > "$prefix/.configure-result.$$" 2>&1
  configure_status=$?
  set -e
  configuration_detail=$(tr '\n' ' ' < "$prefix/.configure-result.$$")
  rm -f "$prefix/.configure-result.$$"
  if [ "$configure_status" -ne 0 ]; then
    echo "result=agent_blocked"; echo "installed=yes"; echo "configured=no"; echo "loaded=no"; echo "verified=no"; echo "next_action=client registration was not applied safely: $configuration_detail"; exit 3
  fi
  configured=yes
fi

echo "result=installed"
echo "version=$version"
echo "binary_path=$prefix/current/bin/wecom-mcp-v2"
echo "sha256=$(sha256 "$prefix/current/bin/wecom-mcp-v2")"
echo "config_path=${service_config:-missing}"
echo "rollback_target=$previous"
echo "installed=yes"
echo "configured=$configured"
echo "loaded=no"
echo "verified=no"
echo "configuration_detail=$configuration_detail"
echo "next_action=restart each registered client; only a runtime initialize, tools/list, and read-only wecom_schema_status tools/call can set loaded/verified to yes"
