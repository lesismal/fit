# fit

Before writing or changing code here, read [docs/rules.md](docs/rules.md) and follow every rule in
it: nothing blocks a fib worker, bodies are streamed with `OnBody` and fib's asynchronous clients,
every buffer, queue and resource is bounded, and every data plane feature is tested against slow
peers, large bodies and cut connections.

For iteration work, also read [docs/progress.md](docs/progress.md) and the iteration's section in
[docs/dev-plan.md](docs/dev-plan.md); when an iteration is done, update both dev-plan files and
both progress files.
