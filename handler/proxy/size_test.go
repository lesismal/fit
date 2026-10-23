//go:build !race

package proxy

// The stream tests move streamSize bytes each way and fail when the heap
// rises more than maxHeapRise while they do. The heap is the test's too:
// its HTTP/2 client and backends hold flow-control windows and TLS buffers
// of their own. Over h2c the rise was measured from 0 to 29 MB, the same
// moving 256 MB as 1 GB, and 34 MB on a slower CI runner: the limit leaves
// room for that and is still a quarter of a body held whole.
const (
	streamSize  = 256 << 20
	maxHeapRise = 64 << 20
)
