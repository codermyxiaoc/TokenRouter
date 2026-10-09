#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
TEST_ROOT=$(mktemp -d)
trap 'rm -rf -- "$TEST_ROOT"' EXIT
SCRIPT="$ROOT_DIR/deploy/install.sh"

fail() {
    printf 'FAIL: %s\n' "$*" >&2
    exit 1
}

# 只提取安装器函数，在隔离临时目录模拟资源下载；不启动服务或使用真实网络。
sed -n '/^GITHUB_REPO=/p; /^get_latest_version() {$/,/^}$/p; /^get_current_version() {$/,/^}$/p; /^release_is_newer() {$/,/^}$/p; /^release_download_curl() {$/,/^}$/p; /^download_and_extract() {$/,/^}$/p; /^upgrade() {$/,/^}$/p; /^install_version() {$/,/^}$/p; /^validate_version() {$/,/^}$/p' "$SCRIPT" > "$TEST_ROOT/functions.sh"
source "$TEST_ROOT/functions.sh"
[[ "$GITHUB_REPO" == codermyxiaoc/TokenRouter ]] || fail "installer default repository is not the fork"

mkdir -p "$TEST_ROOT/bin"
cat > "$TEST_ROOT/bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$CURL_LOG"
[[ -z "${UPDATE_GITHUB_TOKEN:-}${GITHUB_TOKEN:-}${GH_TOKEN:-}" ]] || exit 91
[[ "$1" == -q ]] || exit 92
url="" output=""
while [[ $# -gt 0 ]]; do
    case "$1" in
        -o) output="$2"; shift 2 ;;
        https://*) url="$1"; shift ;;
        *) shift ;;
    esac
done
case "$url" in
    "https://github.com/codermyxiaoc/TokenRouter/releases/download/${TEST_RELEASE_TAG}/"*) ;;
    *) exit 93 ;;
esac
asset="${url##*/}"
[[ -f "$ASSET_DIR/$asset" ]] || exit 22
cp "$ASSET_DIR/$asset" "$output"
EOF
chmod +x "$TEST_ROOT/bin/curl"
export PATH="$TEST_ROOT/bin:$PATH"
export CURL_LOG="$TEST_ROOT/curl.log"
export TEST_RELEASE_TAG=v0.1.278-ct-v2.6
export UPDATE_GITHUB_TOKEN=test-update-token GITHUB_TOKEN=test-fallback-token GH_TOKEN=test-gh-token

msg() { printf '%s' "$1"; }
print_info() { :; }
print_success() { :; }
print_warning() { :; }
print_error() { printf '%s\n' "$*" >&2; }

create_assets() {
    local name=$1 layout=$2 checksum=$3
    ASSET_DIR="$TEST_ROOT/$name/assets"
    INSTALL_DIR="$TEST_ROOT/$name/install"
    export ASSET_DIR
    mkdir -p "$ASSET_DIR" "$INSTALL_DIR" "$TEST_ROOT/$name/content"
    local archive="sub2api_${TEST_RELEASE_TAG}_linux_amd64.tar.gz"
    local package_dir="$TEST_ROOT/$name/content"
    if [[ "$layout" == legacy ]]; then
        archive="sub2api_${TEST_RELEASE_TAG#v}_linux_amd64.tar.gz"
    else
        package_dir="$package_dir/${archive%.tar.gz}"
        mkdir -p "$package_dir"
    fi
    printf '#!/usr/bin/env bash\nprintf "TokenRouter version %s\\n"\n' "${TEST_RELEASE_TAG#v}" > "$package_dir/sub2api"
    chmod +x "$package_dir/sub2api"
    tar -czf "$ASSET_DIR/$archive" -C "$TEST_ROOT/$name/content" .
    local digest
    digest=$(sha256sum "$ASSET_DIR/$archive" | awk '{print $1}')
    case "$checksum" in
        standalone) printf '%s  %s\n' "$digest" "$archive" > "$ASSET_DIR/$archive.sha256" ;;
        manifest) printf '%s  %s\n' "$digest" "$archive" > "$ASSET_DIR/checksums.txt" ;;
        mismatch) printf '%064d  %s\n' 0 "$archive" > "$ASSET_DIR/$archive.sha256" ;;
        wrong-name) printf '%s  unrelated.tar.gz\n' "$digest" > "$ASSET_DIR/$archive.sha256" ;;
        missing) : ;;
    esac
}

run_download() {
    (
        OS=linux ARCH=amd64 LATEST_VERSION="$TEST_RELEASE_TAG"
        download_and_extract
    )
}

create_assets manual nested standalone
run_download
[[ "$("$INSTALL_DIR/sub2api" --version)" == 'TokenRouter version 0.1.278-ct-v2.6' ]] || fail "nested manual package not installed"
[[ "$(get_current_version)" == '0.1.278-ct-v2.6' ]] || fail "fork version suffix was lost"

create_assets goreleaser legacy manifest
run_download
[[ -f "$INSTALL_DIR/sub2api" ]] || fail "legacy root package not installed"

for broken in mismatch wrong-name missing; do
    create_assets "$broken" nested "$broken"
    printf 'original\n' > "$INSTALL_DIR/sub2api"
    if run_download >/dev/null 2>&1; then
        fail "accepted invalid checksum: $broken"
    fi
    [[ "$(cat "$INSTALL_DIR/sub2api")" == original ]] || fail "failed download replaced existing binary"
done

# API 响应即使压缩成单行，也不能把末尾的发行说明误当成标签。
github_api_curl() { printf '{"tag_name":"v0.1.278-ct-v2.6","body":"release notes"}'; }
get_latest_version
[[ "$LATEST_VERSION" == v0.1.278-ct-v2.6 ]] || fail "minified latest response parsed incorrectly"

# API 只允许完整标签；文件下载的每一条 URL 都由 curl 桩严格验证归属。
github_api_curl() { printf 200; }
[[ "$(validate_version 0.1.278-ct-v2.6)" == v0.1.278-ct-v2.6 ]] || fail "fork tag rejected"
[[ "$(validate_version v2.6)" == v2.6 ]] || fail "short product tag rejected"
if validate_version '../../other/release' >/dev/null 2>&1; then fail "unsafe tag accepted"; fi
release_is_newer v0.1.278-ct-v2.6 0.1.278-ct-v2.5 || fail "fork update not detected"
release_is_newer v2.6 0.1.278-ct-v2.5 || fail "short product update not detected"
if release_is_newer v0.1.278-ct-v1.8 0.1.278-ct-v2.5; then fail "older latest treated as upgrade"; fi
if release_is_newer v0.1.278-ct-v2.5 0.1.278-ct-v2.5; then fail "same version treated as upgrade"; fi
if release_is_newer v2.5.0 0.1.278-ct-v2.5; then fail "equivalent short version treated as upgrade"; fi
release_is_newer v0.1.279-ct-v2.5 0.1.278-ct-v2.5 || fail "same-product upstream baseline update not detected"
release_is_newer v0.1.278-ct-v2.10 0.1.278-ct-v2.9 || fail "numeric product ordering is incorrect"

# 显式回退仍经过同一个 fork 下载入口，保留包含完整产品版本的旧二进制备份。
TEST_RELEASE_TAG=v0.1.278-ct-v1.8
create_assets rollback nested standalone
printf '#!/usr/bin/env bash\nprintf "TokenRouter version 0.1.278-ct-v2.6\\n"\n' > "$INSTALL_DIR/sub2api"
chmod +x "$INSTALL_DIR/sub2api"
systemctl() { [[ "$1" != is-active ]]; }
chown() { :; }
SERVICE_USER=test
OS=linux ARCH=amd64
(install_version "$TEST_RELEASE_TAG")
[[ "$(get_current_version)" == '0.1.278-ct-v1.8' ]] || fail "explicit rollback did not install the requested fork version"
[[ -f "$INSTALL_DIR/sub2api.backup.0.1.278-ct-v2.6" ]] || fail "rollback backup lost the fork version suffix"

# 已装版本领先 GitHub latest 时，自动升级必须在停服务和下载前结束。
TEST_RELEASE_TAG=v0.1.278-ct-v2.6
create_assets current nested standalone
run_download
get_latest_version() { LATEST_VERSION=v0.1.278-ct-v1.8; }
systemctl() { fail "upgrade touched service when no update exists"; }
download_and_extract() { fail "upgrade downloaded an older release"; }
upgrade

if grep -qi TokenFlux "$CURL_LOG"; then fail "requested the old repository"; fi
grep -Fq 'https://raw.githubusercontent.com/codermyxiaoc/TokenRouter/main/deploy' "$ROOT_DIR/deploy/docker-deploy.sh" || fail "Docker preparation uses another source"
grep -Fq 'APPLE_CONTAINER_SUB2API_IMAGE=coderxiaoc/tokenrouter:v0.1.278-ct-v3.5' "$ROOT_DIR/deploy/.env.example" || fail "Apple image default is not the fork"
grep -Fq 'read_env_value APPLE_CONTAINER_SUB2API_IMAGE coderxiaoc/tokenrouter:v0.1.278-ct-v3.5' "$ROOT_DIR/deploy/apple-container.sh" || fail "Apple fallback image default is not the fork"

# Apple 平台缺少镜像时明确失败，不悄悄回退旧仓库或 amd64。
sed -n '/^ensure_image_available() {$/,/^}$/p' "$ROOT_DIR/deploy/apple-container.sh" > "$TEST_ROOT/apple-function.sh"
source "$TEST_ROOT/apple-function.sh"
container() { printf '%s\n' "$*" >> "$TEST_ROOT/apple-calls"; return 1; }
info() { :; }
die() { printf '%s\n' "$*" >&2; exit 1; }
PLATFORM=linux/arm64
if (ensure_image_available coderxiaoc/tokenrouter:v0.1.278-ct-v3.5) 2> "$TEST_ROOT/apple-error"; then fail "missing Apple architecture accepted"; fi
grep -Fq 'Configure an image published for linux/arm64' "$TEST_ROOT/apple-error" || fail "missing Apple architecture has no diagnostic"
[[ "$(grep -c '^image pull ' "$TEST_ROOT/apple-calls")" == 1 ]] || fail "Apple pull attempted an implicit fallback"

printf 'Installer fork source, archive layout, checksum and version checks passed.\n'
