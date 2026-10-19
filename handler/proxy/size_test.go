//go:build !race

package proxy

// The stream tests move streamSize bytes each way and fail when the heap
// rises more than maxHeapRise while they do.
const (
	streamSize  = 256 << 20
	maxHeapRise = 16 << 20
)
