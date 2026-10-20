.PHONY: all build test race vet lint fmt fmt-check schema fuzz bench check m2-exit

GO ?= go
STATICCHECK ?= staticcheck

all: check build

build:
	$(GO) build -o bin/fit ./cmd/fit

test:
	$(GO) test ./...

race:
	$(GO) test -race ./...

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

fuzz:
	$(GO) test ./config -run '^$$' -fuzz FuzzLoad -fuzztime 30s

# Runs nginx, Caddy, Traefik and fit through the benchmark harness; needs
# Docker. See bench/README.md.
bench:
	$(GO) run ./bench/cmd/fitbench

check: fmt-check vet lint race
