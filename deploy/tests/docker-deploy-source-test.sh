#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)
TEST_ROOT=$(mktemp -d)
trap 'rm -rf -- "$TEST_ROOT"' EXIT
mkdir -p "$TEST_ROOT/bin" "$TEST_ROOT/deployment"

# 模拟下载及密钥生成，只运行部署文件准备，不调用 Docker 或访问网络。
cat > "$TEST_ROOT/bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
url="" output=""
[[ "$1" == -fsSL ]] || exit 90
while [[ $# -gt 0 ]]; do
    case "$1" in
        -o) output="$2"; shift 2 ;;
        https://*) url="$1"; shift ;;
        *) shift ;;
    esac
done
case "$url" in
    https://raw.githubusercontent.com/codermyxiaoc/TokenRouter/main/deploy/docker-compose.local.yml|https://raw.githubusercontent.com/codermyxiaoc/TokenRouter/main/deploy/.env.example) ;;
    *) exit 91 ;;
esac
printf '%s\n' "$url" >> "$SOURCE_LOG"
cp "$DEPLOY_SOURCE/${url##*/}" "$output"
EOF
cat > "$TEST_ROOT/bin/openssl" <<'EOF'
#!/usr/bin/env bash
printf '%064d\n' 0
EOF
chmod +x "$TEST_ROOT/bin/curl" "$TEST_ROOT/bin/openssl"
export SOURCE_LOG="$TEST_ROOT/source.log" DEPLOY_SOURCE="$ROOT_DIR/deploy"
(
    cd "$TEST_ROOT/deployment"
    PATH="$TEST_ROOT/bin:$PATH" bash "$ROOT_DIR/deploy/docker-deploy.sh" >/dev/null
)
[[ "$(wc -l < "$SOURCE_LOG" | tr -d ' ')" == 2 ]]
cmp "$TEST_ROOT/deployment/docker-compose.yml" "$ROOT_DIR/deploy/docker-compose.local.yml"
grep -Fq 'SUB2API_IMAGE=coderxiaoc/tokenrouter:v0.1.278-ct-v3.6' "$TEST_ROOT/deployment/.env"
[[ -d "$TEST_ROOT/deployment/data" && -d "$TEST_ROOT/deployment/postgres_data" && -d "$TEST_ROOT/deployment/redis_data" ]]
printf 'Docker deployment fork source checks passed.\n'
