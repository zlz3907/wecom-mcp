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
sh -n scripts/install-portable.sh
sh -n scripts/verify-portable-install.sh
sh -n scripts/rollback-portable.sh
go test ./...
go vet ./...
go build -trimpath -o "$test_root/local-build" ./cmd/wecom-mcp-v2

# package-release.sh intentionally rejects a dirty checkout. Build the fixture
# from a temporary clean commit so this test exercises the current candidate
# without relaxing the real release-publish gate.
package_repo="$test_root/package-source"
git clone --quiet --no-hardlinks "$repo_dir" "$package_repo"
git -C "$repo_dir" diff --binary HEAD > "$test_root/candidate.patch"
if [ -s "$test_root/candidate.patch" ]; then
  git -C "$package_repo" apply "$test_root/candidate.patch"
fi
git -C "$repo_dir" ls-files --others --exclude-standard | while IFS= read -r file; do
  mkdir -p "$package_repo/$(dirname -- "$file")"
  cp "$repo_dir/$file" "$package_repo/$file"
done
git -C "$package_repo" add -A
if ! git -C "$package_repo" diff --cached --quiet; then
  git -C "$package_repo" -c user.name='offline-test' -c user.email='offline-test@example.invalid' commit --quiet -m 'offline candidate fixture'
fi
installer="$package_repo/install.sh"
verify_script="$package_repo/scripts/verify-portable-install.sh"

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
"$package_repo/scripts/package-release.sh" --version v0.0.0-test1 --output "$test_root/release1"
"$package_repo/scripts/package-release.sh" --version v0.0.0-test2 --output "$test_root/release2"
test -f "$test_root/release1/SHA256SUMS"
test -f "$test_root/release1/RELEASE-MANIFEST.txt"
test -f "$test_root/release1/wecom-mcp-v2_v0.0.0-test1_darwin_arm64.tar.gz"
test -f "$test_root/release1/wecom-mcp-v2_v0.0.0-test1_darwin_amd64.tar.gz"
test -f "$test_root/release1/wecom-mcp-v2_v0.0.0-test1_linux_arm64.tar.gz"
test -f "$test_root/release1/wecom-mcp-v2_v0.0.0-test1_linux_amd64.tar.gz"
test "$(wc -l < "$test_root/release1/SHA256SUMS" | tr -d ' ')" -eq 6
grep -q '^asset_linux_amd64=wecom-mcp-v2_v0.0.0-test1_linux_amd64.tar.gz$' "$test_root/release1/RELEASE-MANIFEST.txt"

home_dir="$test_root/home"
mkdir -p "$home_dir/.codex"
printf 'model = "test"\n' > "$home_dir/.codex/config.toml"
service_config="$test_root/zoop_wecom_zhycit.local.json"
cp config/zoop_wecom_zhycit.json.example "$service_config"
install_prefix="$home_dir/.mcp/wecom-mcp-v2"

HOME="$home_dir" "$installer" --version v0.0.0-test1 --prefix "$install_prefix" --client codex --config "$service_config" --release-base "file://$test_root/release1" > "$test_root/first-install.txt"
grep -qx 'result=passed' "$test_root/first-install.txt"
grep -qx 'installed=yes' "$test_root/first-install.txt"
grep -qx 'configured=yes' "$test_root/first-install.txt"
grep -qx 'loaded=no' "$test_root/first-install.txt"
grep -qx 'verified=no' "$test_root/first-install.txt"
grep -q 'zoop_wecom_zhycit' "$home_dir/.codex/config.toml"
test -n "$(find "$home_dir/.codex" -name 'config.toml.wecom-mcp-v2-*.bak' -print -quit)"
"$verify_script" --prefix "$install_prefix"

# A repeat install must not overwrite the release or create a second managed block.
HOME="$home_dir" "$installer" --version v0.0.0-test1 --prefix "$install_prefix" --client codex --config "$service_config" --release-base "file://$test_root/release1" > "$test_root/repeat-install.txt"
test "$(grep -c 'BEGIN wecom-mcp-v2 managed' "$home_dir/.codex/config.toml")" -eq 1

rollback_version=$(basename "$(CDPATH= cd -- "$install_prefix/current" && pwd -P)")
HOME="$home_dir" "$installer" --version v0.0.0-test2 --prefix "$install_prefix" --client none --release-base "file://$test_root/release2" > "$test_root/second-install.txt"
test "$(basename "$(CDPATH= cd -- "$install_prefix/current" && pwd -P)")" != "$rollback_version"

# A locally tampered release is rejected; rollback still works with the intact old release.
printf 'tamper' >> "$install_prefix/current/bin/wecom-mcp-v2"
set +e
HOME="$home_dir" "$installer" --version v0.0.0-test2 --prefix "$install_prefix" --client none --release-base "file://$test_root/release2" > "$test_root/tamper.txt" 2>&1
tamper_exit=$?
set -e
test "$tamper_exit" -ne 0
grep -q 'existing release directory failed manifest/hash verification' "$test_root/tamper.txt"
HOME="$home_dir" "$installer" --prefix "$install_prefix" --rollback "$rollback_version" > "$test_root/rollback.txt"
grep -qx 'result=passed' "$test_root/rollback.txt"
grep -qx 'operation=rollback' "$test_root/rollback.txt"
"$verify_script" --prefix "$install_prefix"

# No configuration is fail-closed: a binary may be installed, but it is neither
# registered nor claimed to be loaded/verified.
no_config_home="$test_root/no-config-home"
HOME="$no_config_home" "$installer" --version v0.0.0-test1 --prefix "$no_config_home/.mcp/wecom-mcp-v2" --client auto --release-base "file://$test_root/release1" > "$test_root/no-config.txt"
grep -qx 'result=passed' "$test_root/no-config.txt"
grep -qx 'configured=no' "$test_root/no-config.txt"
grep -qx 'loaded=no' "$test_root/no-config.txt"
grep -qx 'verified=no' "$test_root/no-config.txt"

# Unknown client and WorkBuddy must stop before creating an installation prefix.
set +e
HOME="$no_config_home" "$installer" --version v0.0.0-test1 --prefix "$test_root/unknown-prefix" --client unknown --release-base "file://$test_root/release1" > "$test_root/unknown-client.txt" 2>&1
unknown_exit=$?
HOME="$no_config_home" "$installer" --version v0.0.0-test1 --prefix "$test_root/workbuddy-prefix" --client workbuddy --release-base "file://$test_root/release1" > "$test_root/workbuddy.txt" 2>&1
workbuddy_exit=$?
set -e
test "$unknown_exit" -ne 0
test "$workbuddy_exit" -ne 0
grep -qx 'result=agent_blocked' "$test_root/unknown-client.txt"
grep -qx 'result=agent_blocked' "$test_root/workbuddy.txt"
test ! -e "$test_root/unknown-prefix"
test ! -e "$test_root/workbuddy-prefix"

# Platform and architecture probes are fail-closed before curl or prefix writes.
fake_bin="$test_root/fake-bin"
mkdir -p "$fake_bin"
printf '%s\n' '#!/bin/sh' 'case "$1" in -s) echo Windows_NT ;; -m) echo x86_64 ;; esac' > "$fake_bin/uname"
chmod 0755 "$fake_bin/uname"
set +e
PATH="$fake_bin:$PATH" HOME="$no_config_home" "$installer" --version v0.0.0-test1 --prefix "$test_root/windows-prefix" --client none --release-base "file://$test_root/release1" > "$test_root/windows-platform.txt" 2>&1
windows_platform_exit=$?
printf '%s\n' '#!/bin/sh' 'case "$1" in -s) echo Darwin ;; -m) echo riscv64 ;; esac' > "$fake_bin/uname"
PATH="$fake_bin:$PATH" HOME="$no_config_home" "$installer" --version v0.0.0-test1 --prefix "$test_root/unknown-arch-prefix" --client none --release-base "file://$test_root/release1" > "$test_root/unknown-arch.txt" 2>&1
unknown_arch_exit=$?
set -e
test "$windows_platform_exit" -ne 0
test "$unknown_arch_exit" -ne 0
grep -qx 'result=agent_blocked' "$test_root/windows-platform.txt"
grep -qx 'result=agent_blocked' "$test_root/unknown-arch.txt"
grep -qx 'platform=unknown/unknown' "$test_root/windows-platform.txt"
grep -qx 'platform=darwin/unknown' "$test_root/unknown-arch.txt"
test ! -e "$test_root/windows-prefix"
test ! -e "$test_root/unknown-arch-prefix"

# A malformed Release checksum is rejected before any local prefix mutation.
cp -R "$test_root/release1" "$test_root/release-bad-checksum"
awk '$2 == "RELEASE-MANIFEST.txt" { sub(/^[0-9a-f]+/, "abc") } { print }' \
  "$test_root/release-bad-checksum/SHA256SUMS" > "$test_root/release-bad-checksum/SHA256SUMS.tmp"
mv "$test_root/release-bad-checksum/SHA256SUMS.tmp" "$test_root/release-bad-checksum/SHA256SUMS"
set +e
HOME="$no_config_home" "$installer" --version v0.0.0-test1 --prefix "$test_root/bad-checksum-prefix" --client none --release-base "file://$test_root/release-bad-checksum" > "$test_root/bad-checksum.txt" 2>&1
checksum_exit=$?
set -e
test "$checksum_exit" -ne 0
grep -qx 'result=agent_blocked' "$test_root/bad-checksum.txt"
test ! -e "$test_root/bad-checksum-prefix"

# The source-based lifecycle entry point must use the same verified-link and
# hash rules without contacting GitHub.
portable_prefix="$test_root/portable-install"
"$package_repo/scripts/install-portable.sh" --prefix "$portable_prefix"
"$package_repo/scripts/verify-portable-install.sh" --prefix "$portable_prefix"
portable_version=$(sed -n 's/^version=//p' "$portable_prefix/current/INSTALL-MANIFEST.txt")
"$package_repo/scripts/rollback-portable.sh" --prefix "$portable_prefix" --version "$portable_version"
"$package_repo/scripts/verify-portable-install.sh" --prefix "$portable_prefix"

echo "github_installer_test=passed"
