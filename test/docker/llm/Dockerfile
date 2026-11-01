# Image of the docker end-to-end test of fit's LLM feature (build context: the
# repository root, with Dockerfile.dockerignore beside this file).
#   target "fit":    fit (every role runs as `fit run -c`), the mock upstream
#                    mockllm, certgen and the fake ollama / nvidia-smi
#   target "tester": the compiled end-to-end test (test/docker/llm/e2e,
#                    build tag e2e)
ARG GO_IMAGE=golang:1.27-bookworm
ARG BASE_IMAGE=debian:bookworm-slim

FROM ${GO_IMAGE} AS build
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY} CGO_ENABLED=0
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    go build -trimpath -o /out/ ./cmd/fit ./test/docker/llm/mockllm ./test/docker/llm/certgen && \
    go test -c -tags e2e -o /out/e2e.test ./test/docker/llm/e2e

FROM ${BASE_IMAGE} AS fit
COPY --from=build /out/fit /out/mockllm /out/certgen /usr/local/bin/
COPY test/docker/llm/fakebin/ /usr/local/bin/
WORKDIR /data

FROM ${BASE_IMAGE} AS tester
COPY --from=build /out/e2e.test /usr/local/bin/e2e.test
ENTRYPOINT ["/usr/local/bin/e2e.test"]
