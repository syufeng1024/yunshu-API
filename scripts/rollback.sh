#!/usr/bin/env bash
#
# gatewayd 回滚。
#
# 默认按镜像回退到上一个 tag；没有 --image 时才用 kubectl rollout undo。
# 回滚前后都读 schema_migrations 的 max(version)：
#   - 回滚后的值必须与回滚前一致，否则说明回滚期间有人跑了迁移；
#   - 传了 --target-schema-version 且当前版本更高时，拒绝回滚并打印前滚指引
#     （迁移不可逆，硬退只会让旧代码读到新 Schema）。
#
# 用法：
#   scripts/rollback.sh --image ghcr.io/yunshu/gatewayd:v1.2.2
#   scripts/rollback.sh --target-schema-version 0021 --dry-run
set -euo pipefail

NAMESPACE=${NAMESPACE:-yunshu}
DEPLOYMENT=${DEPLOYMENT:-gateway-stable}
CONTAINER=${CONTAINER:-gatewayd}
EXPECTED_SCHEMA=${EXPECTED_SCHEMA:-}
TARGET_SCHEMA=${TARGET_SCHEMA:-}
IMAGE=""
DRY_RUN=0
MIGRATE_BIN=/usr/local/bin/migrate

usage() {
    cat <<'EOF'
用法: scripts/rollback.sh [--namespace yunshu] [--deployment gateway-stable]
                          [--container gatewayd] [--image <上一 tag 镜像>]
                          [--expected-schema-version 0021]
                          [--target-schema-version 0021] [--dry-run]

  --image                    指定上一版镜像；省略则执行 kubectl rollout undo
  --expected-schema-version  回滚前读到的 max(version)，回滚后必须相等
  --target-schema-version    目标代码支持的最高迁移版本；当前更高则拒绝回滚
  --dry-run                  只打印计划，不访问集群
EOF
}

die() {
    echo "rollback: $*" >&2
    exit 1
}

log() {
    printf '%s\n' "$*"
}

while [ $# -gt 0 ]; do
    case "$1" in
        --namespace)
            [ $# -ge 2 ] || die "--namespace 需要值"
            NAMESPACE=$2
            shift 2
            ;;
        --deployment)
            [ $# -ge 2 ] || die "--deployment 需要值"
            DEPLOYMENT=$2
            shift 2
            ;;
        --container)
            [ $# -ge 2 ] || die "--container 需要值"
            CONTAINER=$2
            shift 2
            ;;
        --image)
            [ $# -ge 2 ] || die "--image 需要值"
            IMAGE=$2
            shift 2
            ;;
        --expected-schema-version)
            [ $# -ge 2 ] || die "--expected-schema-version 需要值"
            EXPECTED_SCHEMA=$2
            shift 2
            ;;
        --target-schema-version)
            [ $# -ge 2 ] || die "--target-schema-version 需要值"
            TARGET_SCHEMA=$2
            shift 2
            ;;
        --dry-run)
            DRY_RUN=1
            shift
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            die "未知参数 $1"
            ;;
    esac
done

if [ "$DRY_RUN" -eq 0 ]; then
    command -v kubectl >/dev/null 2>&1 || die "kubectl 不在 PATH"
fi

schema_version() {
    kubectl -n "$NAMESPACE" exec "deploy/$DEPLOYMENT" -- "$MIGRATE_BIN" status 2>/dev/null |
        awk '$3=="APPLIED" {v=$1} END{print v}'
}

forward_only() {
    echo "rollback: 拒绝回滚：当前 schema max(version)=${1:-unknown} 高于目标 ${TARGET_SCHEMA}。" >&2
    echo "rollback: 迁移不可逆时只前滚——部署包含该迁移的新镜像，再用 scripts/deploy.sh 重新灰度。" >&2
    exit 2
}

if [ "$DRY_RUN" -eq 1 ]; then
    if [ -n "$IMAGE" ]; then
        log "[dry-run] kubectl -n $NAMESPACE set image deployment/$DEPLOYMENT $CONTAINER=$IMAGE"
    else
        log "[dry-run] kubectl -n $NAMESPACE rollout undo deployment/$DEPLOYMENT"
    fi
    log "[dry-run] kubectl -n $NAMESPACE rollout status deployment/$DEPLOYMENT --timeout=600s"
    log "[dry-run] 校验 schema_migrations max(version) 前后一致（expected=${EXPECTED_SCHEMA:-未指定}）"
    exit 0
fi

BEFORE=$(schema_version || true)
if [ -z "$BEFORE" ]; then
    # 读不到版本就无法证明回滚没有把代码退到旧 Schema，按失败关闭处理。
    die "无法读取 schema_migrations，拒绝回滚（先用 deploy/$DEPLOYMENT 的 migrate status 排查）"
fi
log "回滚前 schema max(version): $BEFORE"

if [ -n "$TARGET_SCHEMA" ] &&
    awk -v cur="$BEFORE" -v target="$TARGET_SCHEMA" 'BEGIN{exit !(cur+0 > target+0)}'; then
    forward_only "$BEFORE"
fi

if [ -n "$IMAGE" ]; then
    kubectl -n "$NAMESPACE" set image deployment/"$DEPLOYMENT" "$CONTAINER=$IMAGE"
else
    kubectl -n "$NAMESPACE" rollout undo deployment/"$DEPLOYMENT"
fi
kubectl -n "$NAMESPACE" rollout status deployment/"$DEPLOYMENT" --timeout=600s

AFTER=$(schema_version || true)
if [ -z "$AFTER" ]; then
    die "回滚后无法读取 schema_migrations，需人工确认"
fi
if [ "$AFTER" != "$BEFORE" ]; then
    die "schema 在回滚期间变化：before=$BEFORE after=$AFTER；禁止继续，按前滚处理"
fi
if [ -n "$EXPECTED_SCHEMA" ] && [ "$AFTER" != "$EXPECTED_SCHEMA" ]; then
    die "schema max(version)=$AFTER 与发布前记录 $EXPECTED_SCHEMA 不一致；按前滚处理"
fi

log "回滚完成：$DEPLOYMENT -> ${IMAGE:-上一 revision}，schema max(version)=$AFTER 未变"
