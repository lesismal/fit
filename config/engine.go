package config

// Engine is how fib's engines are set up: the listeners' and the one
// upstream connections are dialed through each take it. What is left out,
// or 0, keeps fib's own default.
type Engine struct {
	EventLoops     int `json:"event_loops,omitempty" min:"0" max:"1024" doc:"How many event loops each engine waits for its connections' events on, which spread the waits, accepts and wake-ups over cores; the connections' work runs on the workers either way. fib's default when left out or 0: 1 on up to 4 CPUs, 2 on up to 16, and the CPUs divided by 8 on more. Windows has one loop whatever this says."`
	Workers        int `json:"workers,omitempty" min:"0" doc:"The most workers each engine's pool runs connections' reads and callbacks on at once. fib's default, which follows the CPUs, when left out or 0."`
	MinWorkers     int `json:"min_workers,omitempty" min:"0" doc:"How many workers an idle pool keeps, rather than retiring every one and starting them again when work arrives. None when left out or 0."`
	TaskQueue      int `json:"task_queue,omitempty" min:"0" doc:"How many tasks each engine's pool holds waiting for a worker. fib's default, which follows its workers, when left out or 0."`
	PollBatch      int `json:"poll_batch,omitempty" min:"0" doc:"The most events one wait of an event loop collects; the rest wait for the next. 1024 when left out or 0."`
	ReadBufferSize int `json:"read_buffer_size,omitempty" min:"0" max:"67108864" doc:"The bytes a connection reads its socket into at a time. 16 KiB when left out or 0."`
	WriteWatermark int `json:"write_watermark,omitempty" min:"0" doc:"The bytes a connection may have waiting to be written before it stops reading until they drain, the backpressure on one connection; limits.max_pending_bytes bounds them all together. fib's default, 64 KiB, when left out or 0."`
	Backlog        int `json:"backlog,omitempty" min:"0" doc:"The length of each listening socket's queue of connections not yet accepted. The system's when left out or 0."`

	At Value `json:"-" fit:"self"`
}
