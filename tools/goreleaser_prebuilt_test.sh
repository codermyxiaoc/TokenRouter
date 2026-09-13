#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
adapter="$repo_root/tools/goreleaser_prebuilt.sh"
temp_dir=$(mktemp -d)
trap 'rm -rf "$temp_dir"' EXIT HUP INT TERM

# 模拟 upload-artifact 下载后丢失可执行位的预构建二进制。
mkdir -p "$temp_dir/input" "$temp_dir/output"
printf 'prebuilt-binary\n' > "$temp_dir/input/sub2api_linux_amd64"
chmod 0644 "$temp_dir/input/sub2api_linux_amd64"

GORELEASER_PREBUILT_DIR="$temp_dir/input" \
GOOS=linux \
GOARCH=amd64 \
  "$adapter" build -tags=embed -ldflags='-s -w' -o "$temp_dir/output/sub2api" ./cmd/server

cmp "$temp_dir/input/sub2api_linux_amd64" "$temp_dir/output/sub2api"
[ -x "$temp_dir/output/sub2api" ] || {
  printf 'goreleaser prebuilt adapter test failed: output is not executable\n' >&2
  exit 1
}

# 缺少目标产物时必须失败，避免发布出错误架构或空二进制。
if GORELEASER_PREBUILT_DIR="$temp_dir/input" GOOS=darwin GOARCH=arm64 \
  "$adapter" build -o "$temp_dir/output/missing" ./cmd/server 2>/dev/null; then
  printf 'goreleaser prebuilt adapter test failed: missing binary was accepted\n' >&2
  exit 1
fi

printf 'goreleaser prebuilt adapter test passed\n'
