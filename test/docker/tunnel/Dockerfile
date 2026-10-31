# The image of the tunnel's docker test environment (see
# test/docker/tunnel/fit-tunnel-docker.sh): fit built from this checkout,
# the tools the devices and the tester use, and a self-signed certificate
# the roles' consoles are served with, since a console off the loopback
# needs TLS. Every container of the environment runs it.
#
# 隧道 docker 测试环境（见 test/docker/tunnel/fit-tunnel-docker.sh）的镜像：
# 从本仓库构建的 fit、设备和测试容器用到的工具，以及各角色控制台使用的自签名
# 证书（控制台不在回环地址上时需要 TLS）。环境中的每个容器都运行这个镜像。

ARG GO_IMAGE=golang:1.27-bookworm
ARG BASE_IMAGE=alpine:latest

FROM ${GO_IMAGE} AS build
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY} CGO_ENABLED=0 GOTOOLCHAIN=auto
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
	go build -trimpath -o /out/fit ./cmd/fit && /out/fit version

FROM ${BASE_IMAGE}
RUN apk add --no-cache bash curl jq socat coreutils busybox-extras ca-certificates openssl
RUN mkdir -p /certs /data && openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes -days 3650 \
	-subj /CN=fit-tunnel-test -addext "subjectAltName=DNS:hub-1,DNS:hub-2,DNS:node-lab,DNS:node-office,DNS:alice,DNS:bob,DNS:localhost" \
	-keyout /certs/console.key -out /certs/console.crt && chmod 644 /certs/console.key
COPY --from=build /out/fit /usr/local/bin/fit
COPY test/docker/tunnel/device.sh test/docker/tunnel/tests/run.sh /usr/local/bin/
RUN chmod 755 /usr/local/bin/device.sh /usr/local/bin/run.sh
WORKDIR /data
ENTRYPOINT ["fit"]
