#!/bin/sh
# Install one pinned wecom-mcp-v2 GitHub Release asset. This script never
# contacts Enterprise WeCom and never creates or copies a local credential.
set -eu

PROJECT_REPOSITORY="zlz3907/wecom-mcp"
INSTANCE_NAME="zoop_wecom_zhycit"
platform="unknown/unknown"
release_version="unknown"
installed=no
configured=no
loaded=no
verified=no
binary_path=missing
binary_sha256=missing
config_paths=none
rollback_target=none
evidence=none
operation=install

usage() {
  cat >&2 <<'EOF'
usage: install.sh [--version vX.Y.Z] [options]

Options:
  --client auto|codex|trae-solo-cn|workbuddy|none  Register a known client (default: auto)
  --config /absolute/path/to/zoop_wecom_zhycit.local.json
                                                   Existing local instance configuration; never copied
  --prefix /absolute/path                         Installation prefix (default: $HOME/.mcp/wecom-mcp-v2)
  --rollback VERSION                               Switch current to an already verified local release
  --uninstall                                     Remove only the current symlink; retain releases/configuration
  --release-base URL                              HTTPS release base, or file:// for offline tests only
  --codex-config PATH | --trae-config PATH        Explicit known-client config paths
  --workbuddy-config PATH                         Reports blocked: WorkBuddy contract is not verified
EOF
  exit 2
}

version=""
client="auto"
service_config=""
prefix="${HOME:-}/.mcp/wecom-mcp-v2"
prefix_explicit=no
rollback=""
uninstall=no
release_base=""
codex_config=""
trae_config=""
workbuddy_config=""
github_auth_file=""
release_api_json=""

while [ "$#" -gt 0 ]; do
  case "$1" in
    --version) [ "$#" -ge 2 ] || usage; version=$2; shift 2 ;;
    --client) [ "$#" -ge 2 ] || usage; client=$2; shift 2 ;;
    --config) [ "$#" -ge 2 ] || usage; service_config=$2; shift 2 ;;
    --prefix) [ "$#" -ge 2 ] || usage; prefix=$2; prefix_explicit=yes; shift 2 ;;
    --rollback) [ "$#" -ge 2 ] || usage; rollback=$2; shift 2 ;;
    --uninstall) uninstall=yes; operation=uninstall; shift ;;
    --release-base) [ "$#" -ge 2 ] || usage; release_base=$2; shift 2 ;;
    --codex-config) [ "$#" -ge 2 ] || usage; codex_config=$2; shift 2 ;;
    --trae-config) [ "$#" -ge 2 ] || usage; trae_config=$2; shift 2 ;;
    --workbuddy-config) [ "$#" -ge 2 ] || usage; workbuddy_config=$2; shift 2 ;;
    -h|--help) usage ;;
    *) usage ;;
  esac
done

redact_path() {
  redact_value=$1
  if [ -n "${HOME:-}" ]; then
    case "$redact_value" in
      "$HOME"/*) printf '$HOME/%s' "${redact_value#"$HOME"/}"; return ;;
    esac
  fi
  case "$redact_value" in
    "$prefix"/*) printf '$PREFIX/%s' "${redact_value#"$prefix"/}" ;;
    *) printf '<absolute-path>' ;;
  esac
}

refresh_config_paths() {
  config_paths=none
  if [ -n "$service_config" ]; then
    config_paths="service:$(redact_path "$service_config")"
  fi
  if [ -n "$codex_config" ]; then
    config_paths="$config_paths,client:$(redact_path "$codex_config")"
  fi
  if [ -n "$trae_config" ]; then
    config_paths="$config_paths,client:$(redact_path "$trae_config")"
  fi
}

emit() {
  echo "result=$1"
  echo "operation=$operation"
  echo "release_version=$release_version"
  echo "platform=$platform"
  echo "installed=$installed"
  echo "configured=$configured"
  echo "loaded=$loaded"
  echo "verified=$verified"
  echo "binary_path=$binary_path"
  echo "binary_sha256=$binary_sha256"
  refresh_config_paths
  echo "config_paths=$config_paths"
  echo "rollback_target=$rollback_target"
  echo "evidence=$evidence"
  echo "next_action=$2"
}

blocked() {
  blocked_action=$1
  blocked_code=${2:-3}
  emit agent_blocked "$blocked_action"
  exit "$blocked_code"
}

failed() {
  failed_action=$1
  failed_code=${2:-1}
  emit failed "$failed_action"
  exit "$failed_code"
}

case "$prefix" in /*) ;; *) failed "--prefix must be absolute" 2 ;; esac
case "$client" in auto|codex|trae-solo-cn|workbuddy|none) ;; *) blocked "unsupported --client; choose auto, codex, trae-solo-cn, or none" 2 ;; esac
if [ "$prefix_explicit" = no ] && [ -z "${HOME:-}" ]; then
  blocked "HOME is unset; supply an explicit absolute --prefix" 3
fi
if [ -n "$version" ]; then
  case "$version" in v[0-9]*) ;; *) failed "--version must be a fixed release version such as v1.2.3" 2 ;; esac
  case "$version" in *[!A-Za-z0-9._-]*) failed "--version contains unsupported characters" 2 ;; esac
fi
if [ -n "$service_config" ]; then
  case "$service_config" in /*) ;; *) blocked "--config must be an absolute path" 2 ;; esac
  [ -f "$service_config" ] || blocked "local instance config is missing; provide an existing protected *.local.json; no release was changed" 3
  [ -r "$service_config" ] || blocked "local instance config is not readable; fix permissions without printing its contents" 3
  [ ! -L "$service_config" ] || blocked "local instance config must not be a symlink; provide the real protected file path" 3
fi
if [ "$client" = workbuddy ]; then
  blocked "WorkBuddy MCP configuration contract is unverified; inspect its official client contract and add a reviewed adapter before retrying" 3
fi

sha256() {
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print tolower($1)}'
  elif command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print tolower($1)}'
  else
    blocked "install shasum or sha256sum before retrying; no files were changed" 3
  fi
}

manifest_value() {
  manifest_file=$1
  manifest_key=$2
  awk -v key="$manifest_key" '
    BEGIN { prefix = key "="; count = 0 }
    index($0, prefix) == 1 { count++; value = substr($0, length(prefix) + 1) }
    END { if (count != 1 || value == "") exit 1; print value }
  ' "$manifest_file"
}

checksum_entry() {
  checksum_file=$1
  checksum_name=$2
  awk -v wanted="$checksum_name" '
    NF == 2 && $2 == wanted { count++; value = tolower($1) }
    END {
      if (count != 1 || length(value) != 64 || value !~ /^[0-9a-f]+$/) exit 1
      print value
    }
  ' "$checksum_file"
}

valid_sha256() {
  valid_value=$1
  case "$valid_value" in *[!0-9a-f]*|'') return 1 ;; esac
  [ "${#valid_value}" -eq 64 ]
}

curl_https() {
  if [ -n "$github_auth_file" ]; then
    curl -fsSL --proto '=https' --tlsv1.2 --netrc-file "$github_auth_file" "$@"
  else
    curl -fsSL --proto '=https' --tlsv1.2 "$@"
  fi
}

fetch() {
  fetch_url=$1
  fetch_output=$2
  case "$fetch_url" in
    https://*)
      if [ -n "$github_auth_file" ] && [ -n "$release_api_json" ]; then
        case "$fetch_url" in
          "$release_base"/*)
            fetch_asset_name=${fetch_url#"$release_base"/}
            fetch_asset_id=$(asset_id_for_name "$fetch_asset_name") || return 1
            curl_https -H 'Accept: application/octet-stream' "https://api.github.com/repos/$PROJECT_REPOSITORY/releases/assets/$fetch_asset_id" -o "$fetch_output"
            ;;
          *) curl_https "$fetch_url" -o "$fetch_output" ;;
        esac
      else
        curl_https "$fetch_url" -o "$fetch_output"
      fi
      ;;
    file://*) curl -fsSL "$fetch_url" -o "$fetch_output" ;;
    *) return 1 ;;
  esac
}

asset_id_for_name() {
  asset_wanted=$1
  awk -v wanted="$asset_wanted" '
    /"id": [0-9]+,/ {
      current = $0
      sub(/.*"id": /, "", current)
      sub(/,.*/, "", current)
    }
    /"name":/ {
      current_name = $0
      sub(/.*"name": "/, "", current_name)
      sub(/".*/, "", current_name)
      if (current_name == wanted) {
        count++
        value = current
      }
    }
    END {
      if (count != 1 || value == "") exit 1
      print value
    }
  ' "$release_api_json"
}

validate_release_base() {
  case "$release_base" in
    https://*|file://*) ;;
    *) blocked "release base must use HTTPS; file:// is accepted only for local offline tests" 3 ;;
  esac
  case "$release_base" in *'?'*|*'#'*) blocked "release base must not contain query or fragment components" 2 ;; esac
}

verify_release() {
  verify_directory=$1
  verify_manifest="$verify_directory/INSTALL-MANIFEST.txt"
  verify_binary="$verify_directory/bin/wecom-mcp-v2"
  verify_helper="$verify_directory/bin/wecom-mcp-v2-configure"
  verify_example="$verify_directory/config/zoop_wecom_zhycit.json.example"
  [ -f "$verify_manifest" ] && [ -x "$verify_binary" ] && [ -x "$verify_helper" ] && [ -f "$verify_example" ] || return 1
  [ "$(manifest_value "$verify_manifest" format)" = "wecom-mcp-github-release-v1" ] || return 1
  verify_binary_expected=$(manifest_value "$verify_manifest" binary_sha256) || return 1
  verify_helper_expected=$(manifest_value "$verify_manifest" configure_sha256) || return 1
  verify_example_expected=$(manifest_value "$verify_manifest" config_example_sha256) || return 1
  valid_sha256 "$verify_binary_expected" || return 1
  valid_sha256 "$verify_helper_expected" || return 1
  valid_sha256 "$verify_example_expected" || return 1
  [ "$(sha256 "$verify_binary")" = "$verify_binary_expected" ] || return 1
  [ "$(sha256 "$verify_helper")" = "$verify_helper_expected" ] || return 1
  [ "$(sha256 "$verify_example")" = "$verify_example_expected" ] || return 1
}

switch_current() {
  switch_release=$1
  switch_target="$prefix/releases/$switch_release"
  verify_release "$switch_target" || failed "target release failed local manifest/hash verification; current was retained" 1
  if ! "$switch_target/bin/wecom-mcp-v2-configure" --switch-current "$prefix" --release "$switch_release" >/dev/null 2>&1; then
    blocked "unable to atomically switch current; inspect local prefix and do not retry blindly" 3
  fi
}

current_release_name() {
  [ -L "$prefix/current" ] || return 1
  current_target=$(CDPATH= cd -- "$prefix/current" 2>/dev/null && pwd -P) || return 1
  case "$current_target" in
    "$prefix"/releases/*) basename "$current_target" ;;
    *) return 1 ;;
  esac
}

if [ "$uninstall" = yes ]; then
  [ -d "$prefix" ] || failed "prefix does not exist; no release was changed" 1
  prefix=$(CDPATH= cd -- "$prefix" 2>/dev/null && pwd -P) || blocked "prefix is not accessible; inspect permissions without deleting it" 3
  [ -L "$prefix/current" ] || failed "no managed current symlink to uninstall" 1
  uninstall_target=$(current_release_name) || blocked "current does not point inside the managed releases directory" 3
  rm "$prefix/current" || blocked "permission denied while removing only the current symlink; releases were retained" 3
  installed=no
  loaded=unknown
  verified=unknown
  release_version="$uninstall_target"
  binary_path=missing
  evidence="current symlink removed; releases and client configuration retained"
  emit passed "release files and client configuration were retained; restore a client backup or remove the managed entry manually"
  exit 0
fi

if [ -n "$rollback" ]; then
  operation=rollback
  case "$rollback" in *[!A-Za-z0-9._-]*|'') failed "invalid rollback version" 2 ;; esac
  [ -d "$prefix" ] || failed "prefix does not exist; no release was changed" 1
  prefix=$(CDPATH= cd -- "$prefix" 2>/dev/null && pwd -P) || blocked "prefix is not accessible; inspect permissions without changing it" 3
  [ -d "$prefix/releases/$rollback" ] || failed "rollback release is not installed" 1
  if [ -L "$prefix/current" ]; then
    previous=$(current_release_name) || blocked "current does not point inside the managed releases directory" 3
  else
    previous=none
    [ ! -e "$prefix/current" ] || blocked "current exists but is not a symlink; preserve it and inspect manually" 3
  fi
  switch_current "$rollback"
  release_version="$rollback"
  rollback_target="$rollback"
  installed=yes
  binary_path="$prefix/current/bin/wecom-mcp-v2"
  binary_sha256=$(sha256 "$binary_path")
  evidence="local release manifest and binary/helper/config-example hashes verified; runtime not exercised"
  emit passed "restart registered clients, then run initialize, tools/list, and one read-only wecom_schema_status tools/call; only that runtime evidence can change loaded/verified"
  exit 0
fi

case "$(uname -s 2>/dev/null || echo unknown)" in
  Darwin) platform_os=darwin ;;
  Linux) platform_os=linux ;;
  *) platform_os=unknown; platform_arch=unknown; platform="$platform_os/$platform_arch"; blocked "unsupported OS; no release was changed" 3 ;;
esac
case "$(uname -m 2>/dev/null || echo unknown)" in
  arm64|aarch64) platform_arch=arm64 ;;
  x86_64|amd64) platform_arch=amd64 ;;
  *) platform_arch=unknown; platform="$platform_os/$platform_arch"; blocked "unsupported architecture; no release was changed" 3 ;;
esac
platform="$platform_os/$platform_arch"

command -v curl >/dev/null 2>&1 || blocked "curl is unavailable; install it before retrying" 3
command -v tar >/dev/null 2>&1 || blocked "tar is unavailable; install it before retrying" 3
command -v mktemp >/dev/null 2>&1 || blocked "mktemp is unavailable; install it before retrying" 3

github_token="${GH_TOKEN:-${GITHUB_TOKEN:-}}"
if [ -n "$github_token" ]; then
  github_auth_file=$(mktemp "${TMPDIR:-/tmp}/wecom-mcp-github-auth.XXXXXX") || blocked "unable to create a private GitHub auth file; no files were changed" 3
  chmod 0600 "$github_auth_file" || blocked "unable to protect the private GitHub auth file; no files were changed" 3
  printf 'machine github.com\nlogin x-access-token\npassword %s\nmachine api.github.com\nlogin x-access-token\npassword %s\n' "$github_token" "$github_token" > "$github_auth_file" || blocked "unable to prepare private GitHub authentication; no files were changed" 3
  cleanup_auth() { rm -f "$github_auth_file"; }
  trap cleanup_auth EXIT HUP INT TERM
fi

if [ -z "$version" ]; then
  [ -z "$release_base" ] || blocked "--version is required when --release-base is supplied; check the fixed tag before retrying" 2
  resolved_url=$(curl_https -o /dev/null -w '%{url_effective}' "https://github.com/$PROJECT_REPOSITORY/releases/latest") || {
    blocked "unable to resolve the latest GitHub Release; supply --version vX.Y.Z after checking release access" 3
  }
  case "$resolved_url" in
    */releases/tag/*) version=${resolved_url##*/releases/tag/} ;;
    *) blocked "GitHub latest-release redirect did not resolve to a fixed tag" 3 ;;
  esac
  case "$version" in v[0-9]*) ;; *) blocked "resolved release tag is not a supported fixed version" 3 ;; esac
  case "$version" in *[!A-Za-z0-9._-]*) blocked "resolved release tag contains unsupported characters" 3 ;; esac
fi

release_base=${release_base%/}
if [ -z "$release_base" ]; then
  release_base="https://github.com/$PROJECT_REPOSITORY/releases/download/$version"
fi
validate_release_base
asset="wecom-mcp-v2_${version}_${platform_os}_${platform_arch}.tar.gz"
release_name="${version}-${platform_os}-${platform_arch}"
release_version="$version"

temporary_dir=$(mktemp -d "${TMPDIR:-/tmp}/wecom-mcp-install.XXXXXX") || blocked "unable to create a private temporary directory; inspect TMPDIR permissions" 3
cleanup() {
  rm -rf "$temporary_dir"
  if [ -n "$github_auth_file" ]; then rm -f "$github_auth_file"; fi
}
trap cleanup EXIT HUP INT TERM
checksums="$temporary_dir/SHA256SUMS"
release_manifest="$temporary_dir/RELEASE-MANIFEST.txt"
archive="$temporary_dir/$asset"
if [ -n "$github_auth_file" ]; then
  release_api_json="$temporary_dir/GITHUB-RELEASE.json"
  curl_https "https://api.github.com/repos/$PROJECT_REPOSITORY/releases/tags/$version" -o "$release_api_json" || {
    blocked "unable to access the private GitHub Release API; confirm GitHub login and repository access, then retry" 3
  }
fi
if ! fetch "$release_base/SHA256SUMS" "$checksums" || ! fetch "$release_base/RELEASE-MANIFEST.txt" "$release_manifest"; then
  blocked "unable to download the fixed Release manifest/checksum; confirm Release visibility and tag, then retry" 3
fi
manifest_checksum=$(checksum_entry "$checksums" RELEASE-MANIFEST.txt) || blocked "Release checksum has no unique valid RELEASE-MANIFEST.txt entry" 3
[ "$(sha256 "$release_manifest")" = "$manifest_checksum" ] || failed "Release manifest checksum mismatch; no local release was changed" 1
[ "$(manifest_value "$release_manifest" format)" = "wecom-mcp-github-release-index-v1" ] || failed "Release manifest format is unsupported; no local release was changed" 1
[ "$(manifest_value "$release_manifest" version)" = "$version" ] || failed "Release manifest tag does not match the selected fixed version" 1
[ "$(manifest_value "$release_manifest" installer)" = "install.sh" ] || failed "Release manifest installer contract is invalid" 1
[ "$(manifest_value "$release_manifest" checksums)" = "SHA256SUMS" ] || failed "Release manifest checksum contract is invalid" 1
manifest_asset=$(manifest_value "$release_manifest" "asset_${platform_os}_${platform_arch}") || failed "Release manifest has no unique matching platform asset entry" 1
[ "$manifest_asset" = "$asset" ] || failed "Release manifest has no matching platform asset" 1
checksum_entry "$checksums" install.sh >/dev/null || blocked "Release checksum has no unique valid install.sh entry" 3
expected_archive=$(checksum_entry "$checksums" "$asset") || blocked "Release checksum has no unique valid matching platform asset entry" 3

if [ -d "$prefix/releases/$release_name" ]; then
  prefix=$(CDPATH= cd -- "$prefix" 2>/dev/null && pwd -P) || blocked "installation prefix is not accessible; inspect permissions without changing it" 3
  verify_release "$prefix/releases/$release_name" || blocked "existing release directory failed manifest/hash verification; preserve it and inspect manually" 3
else
  if ! fetch "$release_base/$asset" "$archive"; then
    blocked "unable to download the fixed platform asset; confirm Release visibility and retry" 3
  fi
  [ "$(sha256 "$archive")" = "$expected_archive" ] || failed "downloaded platform asset checksum mismatch; old current release was retained" 1
  staging="$temporary_dir/release"
  mkdir "$staging" || blocked "unable to create archive staging directory" 3
  if ! tar -xzf "$archive" -C "$staging"; then
    failed "Release archive could not be extracted; old current release was retained" 1
  fi
  verify_release "$staging" || failed "Release manifest/content verification failed; old current release was retained" 1
  actual_version=$(manifest_value "$staging/INSTALL-MANIFEST.txt" version) || failed "installed manifest has no unique version" 1
  actual_platform=$(manifest_value "$staging/INSTALL-MANIFEST.txt" platform) || failed "installed manifest has no unique platform" 1
  [ "$actual_version" = "$version" ] && [ "$actual_platform" = "$platform" ] || failed "installed manifest does not match selected platform/version" 1
  mkdir -p "$prefix/releases" || blocked "permission denied creating the installation prefix; no current release was changed" 3
  prefix=$(CDPATH= cd -- "$prefix" 2>/dev/null && pwd -P) || blocked "installation prefix is not accessible; inspect permissions without changing it" 3
  [ ! -e "$prefix/releases/$release_name" ] || blocked "release appeared during installation; preserve it and inspect manually" 3
  mv "$staging" "$prefix/releases/$release_name" || blocked "permission denied while storing the verified release; current was retained" 3
fi

if [ -L "$prefix/current" ]; then
  previous=$(current_release_name) || blocked "current does not point inside the managed releases directory; preserve it and inspect manually" 3
else
  previous=none
  [ ! -e "$prefix/current" ] || blocked "current exists but is not a symlink; preserve it and inspect manually" 3
fi
rollback_target="$previous"
switch_current "$release_name"
installed=yes
binary_path="$prefix/current/bin/wecom-mcp-v2"
binary_sha256=$(sha256 "$binary_path")
evidence="Release manifest, SHA256SUMS, archive, and local manifest/helper/config-example hashes verified; runtime not exercised"

configuration_detail="no local --config supplied; binary installed but no client was registered"
if [ "$client" = none ]; then
  configuration_detail="client registration explicitly skipped"
elif [ -z "$service_config" ] && [ -n "${HOME:-}" ] && [ -f "$HOME/.trae/mcp-config/wecom/$INSTANCE_NAME.local.json" ]; then
  service_config="$HOME/.trae/mcp-config/wecom/$INSTANCE_NAME.local.json"
  [ ! -L "$service_config" ] || blocked "detected local instance config is a symlink; provide the real protected file path" 3
  [ -r "$service_config" ] || blocked "detected local instance config is not readable; fix permissions without printing its contents" 3
  configuration_detail="using detected local instance configuration"
fi
if [ -n "$service_config" ]; then
  set -- --client "$client" --binary "$prefix/current/bin/wecom-mcp-v2" --config "$service_config"
  [ -n "$codex_config" ] && set -- "$@" --codex-config "$codex_config"
  [ -n "$trae_config" ] && set -- "$@" --trae-config "$trae_config"
  [ -n "$workbuddy_config" ] && set -- "$@" --workbuddy-config "$workbuddy_config"
  configure_output="$prefix/.configure-result.$$"
  set +e
  "$prefix/current/bin/wecom-mcp-v2-configure" "$@" > "$configure_output" 2>&1
  configure_status=$?
  set -e
  rm -f "$configure_output"
  if [ "$configure_status" -ne 0 ]; then
    emit agent_blocked "binary is installed and current is switched, but client registration was refused; inspect the client contract/permissions and rerun only after the cause is known" 3
  fi
  configured=yes
  configuration_detail="client registration completed; runtime load and read-only verification remain unproven"
fi

evidence="$evidence; configuration=$configuration_detail"
emit passed "restart each registered client; only current-runtime initialize, tools/list, and a read-only wecom_schema_status tools/call can set loaded/verified to yes"
