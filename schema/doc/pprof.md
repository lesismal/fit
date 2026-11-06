Go's runtime profiles, as `net/http/pprof` serves them, at the Admin API's `/debug/pprof/`: the
CPU profile (`profile?seconds=N`), the heap, allocations, goroutines, threads, blocking, mutexes
and execution traces (`trace?seconds=N`). `seconds` may ask for at most `max_duration`, a minute
by default, since a profile holds its connection to the Admin API while it samples.
Off when left out, and then those paths answer 404. The setting follows every config applied, so
the workbench's Global → Profiling page, or a `PUT /config/pprof`, turns it on and off without a
restart.

The profiles are served on the Admin API's own address, behind its token and, off loopback, its
TLS, as every other endpoint of it is; the console does not serve them. Blocking and mutex
contention are sampled only when `block_profile_rate` and `mutex_profile_fraction` are set,
since sampling them costs every such event a little; leave them out once done looking.

```json
{
  "admin": {},
  "pprof": {"block_profile_rate": 10000, "mutex_profile_fraction": 100},
  "listeners": [{"name": "web", "address": ":8080"}],
  "routes": [{"handler": {"module": "respond"}}]
}
```

With it, `go tool pprof http://127.0.0.1:2019/debug/pprof/profile?seconds=30` takes a CPU
profile, and `go tool pprof http://127.0.0.1:2019/debug/pprof/heap` reads the heap. With
`admin.token_file`, fetch the profile with the token first,
`curl -H "Authorization: Bearer $TOKEN" -o cpu.pprof http://127.0.0.1:2019/debug/pprof/profile?seconds=30`,
and open the file with `go tool pprof cpu.pprof`.
