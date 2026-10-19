//go:build !race

package proxy

// The stream tests move streamSize bytes each way and fail when the heap
// rises more than maxHeapRise while they do. The heap is the test's too:
// its HTTP/2 client and backends hold flow-control windows and TLS buffers
// of their own.
const (
	streamSize  = 256 << 20
	maxHeapRise = 32 << 20
)
