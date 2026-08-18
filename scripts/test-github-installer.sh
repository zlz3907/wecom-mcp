#!/bin/sh
# Offline release/installer test. It uses file:// fixtures and never starts the
# MCP or contacts Enterprise WeCom.
set -eu

script_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
repo_dir=$(CDPATH= cd -- "$script_dir/.." && pwd)
test_root=$(mktemp -d "${TMPDIR:-/tmp}/wecom-mcp-github-installer-test.XXXXXX")
cleanup() { rm -rf "$test_root"; }
trap cleanup EXIT HUP INT TERM

cd "$repo_dir"
sh -n install.sh
sh -n scripts/package-release.sh
go test ./...
go vet ./...
go build -trimpath -o "$test_root/local-build" ./cmd/wecom-mcp-v2

# Windows/amd64 is intentionally a fail-closed MVP gap until the Unix flock
# implementation receives a reviewed Windows lock adapter.
set +e
GOOS=windows GOARCH=amd64 go build -trimpath -o "$test_root/windows-unsupported.exe" ./cmd/wecom-mcp-v2 > "$test_root/windows-build.txt" 2>&1
windows_exit=$?
set -e
test "$windows_exit" -ne 0
grep -q 'Flock' "$test_root/windows-build.txt"

# package-release.sh is the architecture matrix test: all four supported Unix
# targets must build before a release directory is considered publishable.
scripts/package-release.sh --version v0.0.0-test1 --output "$test_root/release1"
scripts/package-release.sh --version v0.0.0-test2 --output "$test_root/release2"
test -f "$test_root/release1/SHA256SUMS"
test -f "$test_root/release1/wecom-mcp-v2_v0.0.0-test1_darwin_arm64.tar.gz"
test -f "$test_root/release1/wecom-mcp-v2_v0.0.0-test1_darwin_amd64.tar.gz"
test -f "$test_root/release1/wecom-mcp-v2_v0.0.0-test1_linux_arm64.tar.gz"
test -f "$test_root/release1/wecom-mcp-v2_v0.0.0-test1_linux_amd64.tar.gz"

home_dir="$test_root/home"
mkdir -p "$home_dir/.codex"
printf 'model = "test"\n' > "$home_dir/.codex/config.toml"
service_config="$test_root/zoop_wecom_zhycit.local.json"
cp config/zoop_wecom_zhycit.json.example "$service_config"
install_prefix="$home_dir/.mcp/wecom-mcp-v2"

HOME="$home_dir" ./install.sh --version v0.0.0-test1 --prefix "$install_prefix" --client codex --config "$service_config" --release-base "file://$test_root/release1" > "$test_root/first-install.txt"
grep -qx 'installed=yes' "$test_root/first-install.txt"
grep -qx 'configured=yes' "$test_root/first-install.txt"
grep -q 'zoop_wecom_zhycit' "$home_dir/.codex/config.toml"
test -n "$(find "$home_dir/.codex" -name 'config.toml.wecom-mcp-v2-*.bak' -print -quit)"
scripts/verify-portable-install.sh --prefix "$install_prefix"

# A repeat install must not overwrite the release or create a second managed block.
HOME="$home_dir" ./install.sh --version v0.0.0-test1 --prefix "$install_prefix" --client codex --config "$service_config" --release-base "file://$test_root/release1" > "$test_root/repeat-install.txt"
test "$(grep -c 'BEGIN wecom-mcp-v2 managed' "$home_dir/.codex/config.toml")" -eq 1

rollback_version=$(basename "$(CDPATH= cd -- "$install_prefix/current" && pwd -P)")
HOME="$home_dir" ./install.sh --version v0.0.0-test2 --prefix "$install_prefix" --client none --release-base "file://$test_root/release2" > "$test_root/second-install.txt"
test "$(basename "$(CDPATH= cd -- "$install_prefix/current" && pwd -P)")" != "$rollback_version"

# A locally tampered release is rejected; rollback still works with the intact old release.
printf 'tamper' >> "$install_prefix/current/bin/wecom-mcp-v2"
set +e
HOME="$home_dir" ./install.sh --version v0.0.0-test2 --prefix "$install_prefix" --client none --release-base "file://$test_root/release2" > "$test_root/tamper.txt" 2>&1
tamper_exit=$?
set -e
test "$tamper_exit" -ne 0
grep -q 'failed manifest verification' "$test_root/tamper.txt"
HOME="$home_dir" ./install.sh --prefix "$install_prefix" --rollback "$rollback_version" > "$test_root/rollback.txt"
grep -qx 'result=rolled_back' "$test_root/rollback.txt"
scripts/verify-portable-install.sh --prefix "$install_prefix"

# No configuration is fail-closed: a binary may be installed, but it is neither
# registered nor claimed to be loaded/verified.
no_config_home="$test_root/no-config-home"
HOME="$no_config_home" ./install.sh --version v0.0.0-test1 --prefix "$no_config_home/.mcp/wecom-mcp-v2" --client none --release-base "file://$test_root/release1" > "$test_root/no-config.txt"
grep -qx 'configured=no' "$test_root/no-config.txt"
grep -qx 'loaded=no' "$test_root/no-config.txt"
grep -qx 'verified=no' "$test_root/no-config.txt"

echo "github_installer_test=passed"
