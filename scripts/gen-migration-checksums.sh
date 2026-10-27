#!/usr/bin/env bash
# 重新生成 internal/platform/migrate/checksums.golden。
#
# 只在“新增迁移”时运行。已应用的迁移不得修改——重生成 golden 不会让线上
# schema_migrations 记录的旧 checksum 变得匹配，只会把故障推迟到部署时。
#
# 用法：bash scripts/gen-migration-checksums.sh
set -euo pipefail

cd "$(dirname "$0")/../internal/platform/migrate" || exit 1
go run gen_checksums.go
