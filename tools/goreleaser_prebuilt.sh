#!/bin/sh
set -eu

# GoReleaser Community 不支持导入预构建产物，此适配器按目标复制 matrix job 生成的二进制。
output_path=
while [ "$#" -gt 0 ]; do
  case "$1" in
    -o)
      shift
      [ "$#" -gt 0 ] || {
        printf 'goreleaser prebuilt adapter: -o requires a path\n' >&2
        exit 1
      }
      output_path=$1
      ;;
    -o=*)
      output_path=${1#-o=}
      ;;
  esac
  shift
done

[ -n "$output_path" ] || {
  printf 'goreleaser prebuilt adapter: output path is missing\n' >&2
  exit 1
}

: "${GORELEASER_PREBUILT_DIR:?goreleaser prebuilt adapter: GORELEASER_PREBUILT_DIR is required}"
: "${GOOS:?goreleaser prebuilt adapter: GOOS is required}"
: "${GOARCH:?goreleaser prebuilt adapter: GOARCH is required}"

source_path="${GORELEASER_PREBUILT_DIR}/sub2api_${GOOS}_${GOARCH}"
[ -f "$source_path" ] || {
  printf 'goreleaser prebuilt adapter: binary not found: %s\n' "$source_path" >&2
  exit 1
}

mkdir -p "$(dirname "$output_path")"
install -m 0755 "$source_path" "$output_path"
