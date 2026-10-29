# fit

The development rules below are loaded into every session and bind every change here; the
summary after them does not replace them.

@docs/rules.md

In short: nothing blocks a fib worker, bodies are streamed with `OnBody` and fib's asynchronous
clients, every buffer, queue and resource is bounded, every data plane feature is tested against
slow peers, large bodies and cut connections, and what fib cannot do yet is written up in
[docs/fib-api-requests.md](docs/fib-api-requests.md) and added to fib (the sibling repository
`../fib`), by fit's sessions too, then pushed to fib's `main`, and fit moves to that commit by
its hash (rule 19), with no tag needed; it is never worked around in fit in breach of the rules.

**Load tests (rules 16-18) never run on this machine.** The benchmark host's Linux is the only
reference: no load tests or benchmarks on Windows, macOS, or Docker on Windows or macOS. Benchmarks (`go test -bench`), A/B
comparisons, perf profiles, and stress or flake reproduction (high `-count`, `-cpu`, GOMAXPROCS
limits, parallel packages under load) run on the benchmark host,
`ssh -S ~/.ssh/fitbench.sock root@192.168.100.51` (Linux, no Go: cross-build with
`GOOS=linux GOARCH=amd64 go build` or `go test -c`, copy over, run there). Locally run only
ordinary tests: `go test -race ./...`, vet, staticcheck. A brief for another session that
involves any of this says so explicitly. A user-level hook refuses such commands locally.

**Verification is sized to the change (rules 20-23):** `make test-changed` while working, the
full `go test -race ./...` once before a push; benchmarks only for the hot path, with
`bench/ab.sh` (started in the background, read with `-report` before the push) and
`bench/microbench.sh`; the full harness only at a milestone's end.

For iteration work, also read [docs/progress.md](docs/progress.md) and the iteration's section in
[docs/dev-plan.md](docs/dev-plan.md); when an iteration is done, update both dev-plan files and
both progress files.
