.PHONY: all build test race test-changed vet lint fmt fmt-check schema fuzz bench check m2-exit m3-exit

GO ?= go
STATICCHECK ?= staticcheck

all: check build

build:
	$(GO) build -o bin/fit ./cmd/fit

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

# The race detector on the packages changed since BASE (origin/main) and those
# that depend on them (docs/rules.md rule 20): the check while working; `race`
# runs once before a push.
BASE ?= origin/main
test-changed:
	scripts/test-changed.sh $(BASE)

vet:
	$(GO) vet ./...

lint:
	$(STATICCHECK) ./...

fmt:
	gofmt -w .

fmt-check:
	@unformatted=$$(gofmt -l .); if [ -n "$$unformatted" ]; then echo "These files need gofmt:"; echo "$$unformatted"; exit 1; fi

# Regenerate schema/fit.schema.json after changing a config type, a struct tag
# or a standard module.
schema:
	$(GO) test ./schema -run TestSchemaUpToDate -update

# M2's exit criteria (docs/roadmap.md): a churn of config swaps under traffic,
# a config with only a host name served over HTTPS with a certificate from
# Pebble, and an upstream that stops answering probes leaving rotation and
# coming back. The certificate test needs Pebble; see internal/pebbletest.
M2_EXIT = TestChurn|TestAutoHTTPSFromPebble|TestHealthCheckRotation

m2-exit:
	$(GO) test -race -count=1 -v -run '^($(M2_EXIT))$$' ./test/e2e

# M3's exit criteria (docs/roadmap.md): no middleware or handler blocks a
# worker, and each has a benchmark (test/workers); WebSocket, gRPC (unary and
# streaming) and TCP/UDP proxying, with backpressure under slow peers; and
# one port serving HTTP/1.1, HTTP/2, TLS passed through by SNI and SOCKS.
M3_EXIT = TestEveryModuleCovered|TestNoModuleBlocks|TestWebSocketEcho|TestWebSocketBackpressure|TestWebSocketExtendedConnect|TestGRPCUnary|TestGRPCServerStreaming|TestGRPCClientStreaming|TestGRPCBidiStreaming|TestGRPCBackpressure|TestTCPEcho|TestTCPSlowClient|TestTCPSlowUpstream|TestUDPEcho|TestSNIRouting|TestSOCKS|TestSniffClear|TestOnePortManyProtocols
M3_EXIT_PKGS = ./test/workers ./handler/proxy ./l4 ./test/e2e

m3-exit:
	$(GO) test -race -count=1 -v -run '^($(M3_EXIT))$$' $(M3_EXIT_PKGS)

fuzz:
	$(GO) test ./config -run '^$$' -fuzz FuzzLoad -fuzztime 30s

# Runs nginx, Caddy, Traefik and fit through the benchmark harness; needs
# Docker, on Linux: on the benchmark host (docs/rules.md rules 16-18). For a
# change, bench/ab.sh and bench/microbench.sh instead (rule 21). See
# bench/README.md.
bench:
	$(GO) run ./bench/cmd/fitbench

check: fmt-check vet lint race
