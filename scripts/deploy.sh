#!/usr/bin/env bash
#
# gatewayd 灰度发布。
#
# 前提：
#   - 集群里已有 gateway-stable（deploy/k8s/gatewayd.yaml）和 gateway-canary
#     （deploy/k8s/canary/gateway-canary.yaml）两个 Deployment，两者的 Pod
#     标签分别为 app=gatewayd,pool=stable 与 app=gatewayd,pool=canary；
#     镜像容器名统一为 gatewayd。
#   - 前置 nginx 的配置与 deploy/nginx/gateway.conf 同步。
#
# 每一步的顺序固定：改 split 百分比 → 滚动 canary → 等 ready → 观察窗口 →
# 抓指标比对基线。命中任一阈值即调用 rollback.sh 并 exit 1。
#
# 用法：
#   scripts/deploy.sh --tag v1.2.3 --step 100 --observe 300
#   scripts/deploy.sh --tag v1.2.3 --step 10 --dry-run
#
# --step 10 只跑到 10%；--step 50 依次跑 10、50；--step 100 依次跑 10、50、100。
# 100% 那一档观察通过后把 stable 升到同一 tag，再把 canary 权重降到最小非零值
# （nginx 不接受 0%），canary 保持 1 副本且已是同一 tag。
set -euo pipefail

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
REPO_ROOT=$(cd "$SCRIPT_DIR/.." && pwd)

NAMESPACE=${NAMESPACE:-yunshu}
STABLE_DEPLOY=${STABLE_DEPLOY:-gateway-stable}
CANARY_DEPLOY=${CANARY_DEPLOY:-gateway-canary}
CONTAINER=${CONTAINER:-gatewayd}
IMAGE_REPO=${IMAGE_REPO:-ghcr.io/yunshu/gatewayd}
NGINX_CONF=${NGINX_CONF:-$REPO_ROOT/deploy/nginx/gateway.conf}
NGINX_CONFIGMAP=${NGINX_CONFIGMAP:-}
NGINX_DEPLOY=${NGINX_DEPLOY:-gateway-nginx}
METRICS_PORT=${METRICS_PORT:-9090}
METRICS_TOKEN=${GATEWAY_METRICS_TOKEN:-}
MIGRATE_BIN=/usr/local/bin/migrate
# nginx 不接受 0% 的 split_clients 权重（实测 invalid percent value）。发布
# 完成后留 0.01% 残余流量；此时 canary 已经跑同一个新 tag，不存在版本偏差。
MIN_CANARY_PCT=${MIN_CANARY_PCT:-0.01}

TAG=""
TARGET_STEP=100
OBSERVE=300
DRY_RUN=0

usage() {
    cat <<'EOF'
用法: scripts/deploy.sh --tag <image-tag> [--step 10|50|100] [--observe 300] [--dry-run]

  --tag <image-tag>   要发布的镜像 tag（必填）
  --step 10|50|100    灰度上限，依次执行 10、50、100（默认 100）
  --observe <秒>      每一步观察窗口（默认 300）
  --dry-run           只打印计划与指标占位，不改文件、不访问集群
EOF
}

die() {
    echo "deploy: $*" >&2
    exit 1
}

log() {
    printf '%s\n' "$*"
}

while [ $# -gt 0 ]; do
    case "$1" in
        --tag)
            [ $# -ge 2 ] || die "--tag 需要值"
            TAG=$2
            shift 2
            ;;
        --step)
            [ $# -ge 2 ] || die "--step 需要值"
            TARGET_STEP=$2
            shift 2
            ;;
        --observe)
            [ $# -ge 2 ] || die "--observe 需要值"
            OBSERVE=$2
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

[ -n "$TAG" ] || die "--tag 必填"
case "$TARGET_STEP" in
    10|50|100) ;;
    *) die "--step 只能是 10、50 或 100" ;;
esac
case "$OBSERVE" in
    ''|*[!0-9]*) die "--observe 必须是正整数秒" ;;
esac
[ -f "$NGINX_CONF" ] || die "找不到 nginx 配置: $NGINX_CONF"
if [ "$DRY_RUN" -eq 0 ]; then
    command -v kubectl >/dev/null 2>&1 || die "kubectl 不在 PATH"
fi

IMAGE="$IMAGE_REPO:$TAG"
STABLE_SELECTOR="app=gatewayd,pool=stable"
CANARY_SELECTOR="app=gatewayd,pool=canary"

# 改写 split_clients 里唯一那一行百分比。改完立刻回读，避免 sed 没匹配上
# 却继续 rollout —— 那样 canary 拿到的是 0 流量，观察窗口等于空转。
set_split() {
    pct=$1
    tmp=$(mktemp "${TMPDIR:-/tmp}/gateway.conf.XXXXXX")
    sed -E "s/^([[:space:]]*)[0-9]+(\.[0-9]+)?%([[:space:]]+)canary;/\1${pct}%\3canary;/" \
        "$NGINX_CONF" >"$tmp"
    if ! grep -Eq "^[[:space:]]*${pct}%[[:space:]]+canary;" "$tmp"; then
        rm -f "$tmp"
        die "未能把 $NGINX_CONF 的 canary 比例改成 ${pct}%"
    fi
    mv "$tmp" "$NGINX_CONF"
}

reload_nginx() {
    if [ -z "$NGINX_CONFIGMAP" ]; then
        log "WARN: NGINX_CONFIGMAP 未设置；已改写 $NGINX_CONF，请由平台 reload nginx"
        return 0
    fi
    kubectl -n "$NAMESPACE" create configmap "$NGINX_CONFIGMAP" \
        --from-file=gateway.conf="$NGINX_CONF" --dry-run=client -o yaml |
        kubectl apply -f -
    kubectl -n "$NAMESPACE" rollout restart deployment/"$NGINX_DEPLOY"
    kubectl -n "$NAMESPACE" rollout status deployment/"$NGINX_DEPLOY" --timeout=300s
}

apply_split() {
    pct=$1
    if [ "$DRY_RUN" -eq 1 ]; then
        log "[dry-run] sed -E 's/^([[:space:]]*)[0-9]+(\\.[0-9]+)?%([[:space:]]+)canary;/\\1${pct}%\\3canary;/' $NGINX_CONF"
        return 0
    fi
    set_split "$pct"
    reload_nginx
    log "split_clients canary=${pct}%"
}

gateway_pods() {
    kubectl -n "$NAMESPACE" get pods -l "$1" -o name | sed 's#^pod/##'
}

fetch_pool_metrics() {
    selector=$1
    out=$2
    : >"$out"
    pods=$(gateway_pods "$selector")
    [ -n "$pods" ] || die "selector $selector 没有 Pod"
    for pod in $pods; do
        if [ -n "$METRICS_TOKEN" ]; then
            kubectl -n "$NAMESPACE" exec "$pod" -- \
                wget -q -O - --header="Authorization: Bearer $METRICS_TOKEN" \
                "http://127.0.0.1:${METRICS_PORT}/metrics" >>"$out" ||
                die "抓取 $pod /metrics 失败"
        else
            kubectl -n "$NAMESPACE" exec "$pod" -- \
                wget -q -O - "http://127.0.0.1:${METRICS_PORT}/metrics" >>"$out" ||
                die "抓取 $pod /metrics 失败"
        fi
        printf '\n' >>"$out"
    done
}

metrics_requests_total() {
    awk '/^gw_requests_total\{/ {s+=$NF} END{printf "%.0f", s+0}' "$1"
}

metrics_requests_5xx() {
    # http_status 是 3 位数字标签；=~"5.." 在脚本里展开成正则。
    awk '/^gw_requests_total\{/ && /http_status="5[0-9][0-9]"/ {s+=$NF} END{printf "%.0f", s+0}' "$1"
}

metrics_unavailable() {
    awk '/^gw_ratelimit_unavailable_total\{/ {s+=$NF} END{printf "%.0f", s+0}' "$1"
}

# P99 从 _bucket 的累计计数推出来：按 le 升序累加，第一个 >= 99% 总数的
# 上界就是 P99 的桶上沿。没有样本时输出 n/a，不伪造 0。
metrics_p99() {
    file=$1
    total=$(awk '/^gw_request_duration_seconds_count\{/ {s+=$NF} END{printf "%.0f", s+0}' "$file")
    if [ "$total" -eq 0 ]; then
        echo "n/a"
        return 0
    fi
    target=$(awk -v t="$total" 'BEGIN{printf "%.0f", t*0.99}')
    sed -n 's/^gw_request_duration_seconds_bucket{.*,le="\([^"]*\)"} \([0-9][0-9]*\)$/\1 \2/p' "$file" |
        grep -v '^+Inf ' |
        awk '{sum[$1]+=$2} END{for (le in sum) printf "%s %d\n", le, sum[le]}' |
        sort -n -k1,1 |
        awk -v target="$target" '
            {cum+=$2; if (cum >= target) {print $1; found=1; exit}}
            END{if (!found) print "+Inf"}
        '
}

restart_count() {
    selector=$1
    kubectl -n "$NAMESPACE" get pods -l "$selector" \
        -o jsonpath='{range .items[*].status.containerStatuses[*]}{.restartCount}{"\n"}{end}' |
        awk '{s+=$1} END{print s+0}'
}

schema_version() {
    kubectl -n "$NAMESPACE" exec "deploy/$STABLE_DEPLOY" -- "$MIGRATE_BIN" status 2>/dev/null |
        awk '$3=="APPLIED" {v=$1} END{print v}'
}

# 观察一个池，输出四个数字：5xx 比率、unavailable 窗口增量、P99、重启数。
# unavailable 用观察窗口前后的累计值相减，避免把发布前的历史值算进来。
observe_pool() {
    selector=$1
    before=$(mktemp)
    after=$(mktemp)
    fetch_pool_metrics "$selector" "$before"
    sleep "$OBSERVE"
    fetch_pool_metrics "$selector" "$after"

    total=$(metrics_requests_total "$after")
    five=$(metrics_requests_5xx "$after")
    ratio=$(awk -v e="$five" -v t="$total" \
        'BEGIN{if (t == 0) print "0.00"; else printf "%.2f", e*100/t}')
    unavail_before=$(metrics_unavailable "$before")
    unavail_after=$(metrics_unavailable "$after")
    unavail_delta=$((unavail_after - unavail_before))
    [ "$unavail_delta" -ge 0 ] || unavail_delta=0
    p99=$(metrics_p99 "$after")
    restarts=$(restart_count "$selector")
    rm -f "$before" "$after"

    printf '%s %s %s %s\n' "$ratio" "$unavail_delta" "$p99" "$restarts"
}

# 阈值全部是定数：5xx > 1%、unavailable 增量 > 0、P99 > 1.2×基线、重启 > 0。
check_thresholds() {
    ratio=$1
    unavail_delta=$2
    p99=$3
    restarts=$4
    baseline=$5
    reason=""

    if awk -v r="$ratio" 'BEGIN{exit !(r > 1)}'; then
        reason="5xx 比率 ${ratio}% > 1%"
    elif [ "$unavail_delta" -gt 0 ]; then
        reason="gw_ratelimit_unavailable_total 增量 ${unavail_delta} > 0"
    elif [ "$restarts" -gt 0 ]; then
        reason="Pod 重启数 ${restarts} > 0"
    elif [ "$p99" != "n/a" ] && [ "$baseline" != "n/a" ] &&
        awk -v p="$p99" -v b="$baseline" 'BEGIN{exit !(p > b*1.2)}'; then
        reason="P99 ${p99}s > 1.2×基线 ${baseline}s"
    fi

    if [ -n "$reason" ]; then
        log "ABORT: $reason"
        return 1
    fi
    return 0
}

if [ "$DRY_RUN" -eq 0 ]; then
    PREV_IMAGE=$(kubectl -n "$NAMESPACE" get deployment "$STABLE_DEPLOY" \
        -o jsonpath="{.spec.template.spec.containers[?(@.name==\"$CONTAINER\")].image}")
    [ -n "$PREV_IMAGE" ] || PREV_IMAGE=$(kubectl -n "$NAMESPACE" get deployment "$STABLE_DEPLOY" \
        -o jsonpath='{.spec.template.spec.containers[0].image}')
    SCHEMA_BEFORE=$(schema_version || true)
    log "stable 当前镜像: ${PREV_IMAGE:-unknown}"
    log "schema_migrations max(version): ${SCHEMA_BEFORE:-unknown}"

    BASELINE=$(mktemp)
    fetch_pool_metrics "$STABLE_SELECTOR" "$BASELINE"
    BASELINE_P99=$(metrics_p99 "$BASELINE")
    rm -f "$BASELINE"
    log "P99 基线: ${BASELINE_P99}"
else
    PREV_IMAGE=""
    SCHEMA_BEFORE=""
    BASELINE_P99="n/a"
fi

STEPS=""
for step in 10 50 100; do
    if [ "$step" -le "$TARGET_STEP" ]; then
        STEPS="$STEPS $step"
    fi
done

for pct in $STEPS; do
    log "== canary ${pct}% =="
    apply_split "$pct"

    if [ "$DRY_RUN" -eq 1 ]; then
        log "[dry-run] kubectl -n $NAMESPACE set image deployment/$CANARY_DEPLOY $CONTAINER=$IMAGE"
        log "[dry-run] kubectl -n $NAMESPACE rollout status deployment/$CANARY_DEPLOY --timeout=300s"
        log "指标: 5xx=0.00% unavailable_delta=0 p99=n/a restarts=0 (dry-run)"
        if [ "$pct" -eq 100 ]; then
            log "[dry-run] kubectl -n $NAMESPACE set image deployment/$STABLE_DEPLOY $CONTAINER=$IMAGE"
            apply_split "$MIN_CANARY_PCT"
        fi
        continue
    fi

    kubectl -n "$NAMESPACE" set image deployment/"$CANARY_DEPLOY" "$CONTAINER=$IMAGE"
    kubectl -n "$NAMESPACE" rollout status deployment/"$CANARY_DEPLOY" --timeout=300s

    read -r ratio unavail_delta p99 restarts <<EOF
$(observe_pool "$CANARY_SELECTOR")
EOF
    log "指标: 5xx=${ratio}% unavailable_delta=${unavail_delta} p99=${p99} restarts=${restarts}"

    if ! check_thresholds "$ratio" "$unavail_delta" "$p99" "$restarts" "$BASELINE_P99"; then
        log "回滚到 ${PREV_IMAGE:-上一 revision}"
        "$SCRIPT_DIR/rollback.sh" \
            --namespace "$NAMESPACE" \
            --deployment "$STABLE_DEPLOY" \
            --container "$CONTAINER" \
            --image "$PREV_IMAGE" \
            --expected-schema-version "$SCHEMA_BEFORE" || true
        exit 1
    fi

    if [ "$pct" -eq 100 ]; then
        # 100% 观察通过：把 stable 升到同一 tag，确认 ready 后把 canary 权重
        # 收回到最小非零值。canary 保持 1 副本且已经是同一个 tag，残余流量
        # 不会打到旧版本；nginx 不接受 0%，所以不能写 0%。
        kubectl -n "$NAMESPACE" set image deployment/"$STABLE_DEPLOY" "$CONTAINER=$IMAGE"
        kubectl -n "$NAMESPACE" rollout status deployment/"$STABLE_DEPLOY" --timeout=600s
        apply_split "$MIN_CANARY_PCT"
    fi
done

if [ "$TARGET_STEP" -eq 100 ]; then
    log "发布完成: $IMAGE"
else
    log "灰度停在 ${TARGET_STEP}%，stable 未升级（继续用 --step 100 完成发布）"
fi
