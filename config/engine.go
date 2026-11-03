package config

// Engine is how fib's engines are set up: the listeners' and the one
// upstream connections are dialed through each take it. What is left out,
// or 0, keeps fib's own default.
type Engine struct {
	EventLoops     int    `json:"event_loops,omitempty" min:"0" max:"1024" doc:"How many event loops each engine waits for its connections' events on, which spread the waits, accepts and wake-ups over cores; the connections' work runs on the workers either way. fib's default when left out or 0: 1 on up to 4 CPUs, 2 on up to 16, and the CPUs divided by 8 on more. Windows has one loop whatever this says."`
	Workers        int    `json:"workers,omitempty" min:"0" doc:"The most workers each engine's pool runs connections' reads and callbacks on at once. 100 for each CPU fit may use, and at least min_workers, when left out or 0."`
	MinWorkers     int    `json:"min_workers,omitempty" min:"0" doc:"How many workers an idle pool keeps, rather than retiring every one and starting them again when work arrives. None when left out or 0."`
	TaskQueue      int    `json:"task_queue,omitempty" min:"0" doc:"How many tasks each engine's pool holds waiting for a worker. fib's default, which follows its workers, when left out or 0."`
	PollBatch      int    `json:"poll_batch,omitempty" min:"0" default:"1024" doc:"The most events one wait of an event loop collects; the rest wait for the next. 1024 when left out or 0."`
	ReadBufferSize int    `json:"read_buffer_size,omitempty" min:"0" max:"67108864" default:"65536" doc:"The bytes a connection reads its socket into at a time, which is also the most a proxied body is passed on in one write. 64 KiB when left out or 0."`
	WriteWatermark int    `json:"write_watermark,omitempty" min:"0" doc:"The bytes a connection may have waiting to be written before it stops reading until they drain, the backpressure on one connection; limits.max_pending_bytes bounds them all together. When left out or 0, fib sizes it from read_buffer_size and the machine: four read buffers, at least 64 KiB and at most 256 KiB, so 256 KiB at the default 64 KiB read buffer on a machine with enough memory."`
	LoopWait       string `json:"loop_wait,omitempty" enum:"park,block" default:"\"park\"" doc:"How each event loop waits while none of its connections has an event ready: park, in Go's network poller, which gives the loop's CPU to other work at once, but on a busy server looks for the loop's events only when a CPU runs out of work or every 10ms; or block, in epoll_wait, which wakes the loop as soon as one arrives. Choose block for a shorter latency tail near saturation over HTTP/1.1, at the cost of the median and a little h2 throughput: proxying an API at 200,000 requests a second on 6 CPUs, block took the HTTP/1.1 p99 from 4.3 to 2.9-3.9ms and the median from 0.95 to 1.57ms, left h2 and the maximum as they were, and served h2 0.9% less at saturation. Linux only. park when left out."`
	Backlog        int    `json:"backlog,omitempty" min:"0" doc:"The length of each listening socket's queue of connections not yet accepted. The system's when left out or 0."`

	At Value `json:"-" fit:"self"`
}
