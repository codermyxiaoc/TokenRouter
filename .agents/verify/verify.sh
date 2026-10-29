#!/usr/bin/env bash
# TokenRouter 全量验证脚本：编译、vet、wire 校验、分包单测（-p 1 防内存不足）、前端类型检查与单测。
# 日志写入 .agents/verify/logs，供逐项排查；本目录不提交。
set -u
ROOT=/c/Users/ASUS/Desktop/TokenRouter
LOG="$ROOT/.agents/verify/logs"
mkdir -p "$LOG"
rm -f "$LOG"/*.log
summary="$LOG/summary.log"
step() {
  local name="$1"; shift
  echo ">>> $name $(date +%H:%M:%S)" | tee -a "$summary"
  if "$@" >"$LOG/$name.log" 2>&1; then
    echo "PASS $name" | tee -a "$summary"
  else
    echo "FAIL $name (see $LOG/$name.log)" | tee -a "$summary"
  fi
}

cd "$ROOT/backend" || exit 1
step build go build ./...
step vet go vet ./internal/service/ ./internal/repository/ ./internal/handler/... ./internal/server/... ./internal/config/ ./cmd/server/
# wire 只比对生成结果，不保留生成文件，确认手改的 wire_gen.go 与依赖图一致
step wire bash -c 'cp cmd/server/wire_gen.go "$TEMP/wire_gen.before.go" && go run github.com/google/wire/cmd/wire ./cmd/server; rc=$?; diff -u "$TEMP/wire_gen.before.go" cmd/server/wire_gen.go; d=$?; cp "$TEMP/wire_gen.before.go" cmd/server/wire_gen.go; [ $rc -eq 0 ] && [ $d -eq 0 ]'

for pkg in ./internal/config/ ./internal/pkg/... ./internal/server/middleware/ ./internal/server/routes/ ./internal/server/ ./internal/handler/admin/ ./internal/handler/ ./internal/repository/ ./internal/service/; do
  name="unit_$(echo "$pkg" | tr '/.' '__')"
  step "$name" go test -tags=unit -p 1 -count=1 -timeout 40m "$pkg"
done
# 竞态检测只针对本次改动最集中的并发路径，控制耗时与内存
step race_hot go test -tags=unit -race -p 1 -count=1 -timeout 30m -run 'ImageModel|ImageAttempt|AsyncImage|ImageCooldown|LocalImage|ImageStrikes|SmartRouting|PublicImages|Creative|Video' ./internal/service/ ./internal/repository/ ./internal/server/middleware/ ./internal/handler/

cd "$ROOT/frontend" || exit 1
step vue_tsc npx vue-tsc --noEmit
step vitest npx vitest run
echo "done $(date +%H:%M:%S)" | tee -a "$summary"
