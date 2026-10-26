#
# 应用镜像：gatewayd / controld / jobsd / migrate 四合一。
#
# 四个进程共享同一份构建产物，靠 compose 的 command 选择入口，因此镜像只构建
# 一次、只推一次。镜像内不含源码、不含 go 工具链，运行时是 alpine + 四个静态
# 二进制。
#
# 构建上下文是仓库根目录（需要 go.mod / internal / cmd / relaykit 同时可见）。

ARG GO_IMAGE=golang:1.27-alpine
ARG RUNTIME_IMAGE=alpine:3.20

# ---------------------------------------------------------------- build stage
FROM ${GO_IMAGE} AS build

WORKDIR /src

# 国内网络下 goproxy.cn 明显更快；海外可直接传 --build-arg GOPROXY=https://proxy.golang.org,direct
ARG GOPROXY=https://goproxy.cn,direct

# CGO 关掉才能产出真正的静态二进制，运行阶段就不需要 libc。
ENV CGO_ENABLED=0 \
    GOOS=linux \
    GOFLAGS=-trimpath \
    GOPROXY=${GOPROXY} \
    GOTOOLCHAIN=local

# 先只拷贝依赖清单，让 go mod download 这一层可被缓存。
# relaykit 是独立的子模块（replace 到本地目录），必须单独拷贝它的 go.mod。
COPY go.mod go.sum ./
COPY relaykit/go.mod ./relaykit/go.mod
RUN go mod download

# 再拷贝真正要编译的源码。internal/ 下的 go:embed（Lua 脚本、迁移 SQL）
# 都在这层里，不需要额外拷贝 api/ 或 web/。
COPY relaykit ./relaykit
COPY internal ./internal
COPY cmd ./cmd

# 显式列出四个入口，keytool / fakeupstream 不进运行镜像。
RUN go build -ldflags='-s -w' -o /out/ \
        ./cmd/gatewayd \
        ./cmd/controld \
        ./cmd/jobsd \
        ./cmd/migrate \
 && ls -l /out

# -------------------------------------------------------------- runtime stage
FROM ${RUNTIME_IMAGE}

# ca-certificates：gatewayd 出网访问上游必须校验 TLS。
# tzdata：jobsd 的日切/对账按 CST 计算，容器内需要时区库。
RUN apk add --no-cache ca-certificates tzdata \
 && adduser -D -H -u 10001 -s /sbin/nologin yunshu \
 && mkdir -p /app && chown yunshu:yunshu /app

COPY --from=build /out/gatewayd  /usr/local/bin/gatewayd
COPY --from=build /out/controld  /usr/local/bin/controld
COPY --from=build /out/jobsd     /usr/local/bin/jobsd
COPY --from=build /out/migrate   /usr/local/bin/migrate

USER yunshu
WORKDIR /app

# 四个入口用 compose 的 command 覆盖；默认给数据面，避免空 command 起不来。
CMD ["/usr/local/bin/gatewayd"]
