.PHONY: all build test race vet lint fmt fmt-check schema fuzz check

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

fuzz:
	$(GO) test ./config -run '^$$' -fuzz FuzzLoad -fuzztime 30s

check: fmt-check vet lint race
