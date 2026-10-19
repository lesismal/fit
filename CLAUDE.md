# fit

Before writing or changing code here, read [docs/rules.md](docs/rules.md) and follow every rule in
it: nothing blocks a fib worker, bodies are streamed with `OnBody` and fib's asynchronous clients,
every buffer, queue and resource is bounded, every data plane feature is tested against slow peers,
large bodies and cut connections, and what fib cannot do yet is written up in
[docs/fib-api-requests.md](docs/fib-api-requests.md) for the maintainer to add to fib, not changed
in fib or worked around in breach of the rules.

For iteration work, also read [docs/progress.md](docs/progress.md) and the iteration's section in
[docs/dev-plan.md](docs/dev-plan.md); when an iteration is done, update both dev-plan files and
both progress files.
