#!/bin/sh
set -eu

repo_root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
cd "$repo_root"

# 统一输出失败原因，方便从 CI 日志定位配置漂移。
fail() {
  printf 'docker compose postgres test failed: %s\n' "$1" >&2
  exit 1
}

# 使用完整行匹配，确保所有内置 PostgreSQL 的变体使用完全一致的参数表达式。
assert_line() {
  file=$1
  line=$2
  grep -Fqx "$line" "$file" || fail "$file is missing: $line"
}

# 标准版、本地目录版和开发版都内置 PostgreSQL，必须共同传递服务端调优参数。
for compose_file in \
  deploy/docker-compose.yml \
  deploy/docker-compose.local.yml \
  deploy/docker-compose.dev.yml
do
  assert_line "$compose_file" '    shm_size: ${POSTGRES_SHM_SIZE:-1gb}'
  assert_line "$compose_file" '      -c max_connections=${POSTGRES_MAX_CONNECTIONS:-100}'
  assert_line "$compose_file" '      -c shared_buffers=${POSTGRES_SHARED_BUFFERS:-128MB}'
  assert_line "$compose_file" '      -c effective_cache_size=${POSTGRES_EFFECTIVE_CACHE_SIZE:-4GB}'
  assert_line "$compose_file" '      -c maintenance_work_mem=${POSTGRES_MAINTENANCE_WORK_MEM:-64MB}'
done

# 示例配置必须体现生产目标，避免安装脚本再次生成不匹配的连接容量。
assert_line deploy/.env.example 'POSTGRES_SHM_SIZE=1gb'
assert_line deploy/.env.example 'POSTGRES_MAX_CONNECTIONS=512'
assert_line deploy/.env.example 'DATABASE_MAX_OPEN_CONNS=256'
assert_line deploy/.env.example 'DATABASE_MAX_IDLE_CONNS=128'

printf 'docker compose postgres test passed\n'
