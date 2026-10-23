# syntax=docker/dockerfile:1
# fit's container image: fit alone, a static binary on distroless's static
# image, run as its nonroot user (65532). See deploy/README.md.
#
#	docker build -t fit .
#	docker buildx build --platform linux/amd64,linux/arm64 --build-arg VERSION=v0.1.0 -t fit .
#
# The binary is cross-compiled on the builder's platform for each target, so
# a multi-platform build needs no emulation.
ARG GO_VERSION=1.27

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION} AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY . .
ARG TARGETOS TARGETARCH VERSION
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
	CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
	go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/fit ./cmd/fit

FROM gcr.io/distroless/static-debian12:nonroot
LABEL org.opencontainers.image.licenses="Apache-2.0"
COPY --from=build /out/fit /usr/local/bin/fit
COPY LICENSE /usr/share/doc/fit/LICENSE
COPY deploy/docker/fit.json /etc/fit/fit.json
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/fit"]
CMD ["run", "-c", "/etc/fit/fit.json"]
