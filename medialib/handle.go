package medialib

import "os"

// File returns the file the handle reads, for a handler that sends a range
// of it as it is, as fib's SendFile does, without reading it on the pool.
// It stays the handle's: it is closed with Close.
func (h *Handle) File() *os.File { return h.f }
