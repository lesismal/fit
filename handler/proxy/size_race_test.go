//go:build race

package proxy

// Under the race detector, which runs several times slower, the stream tests
// move less.
const (
	streamSize  = 64 << 20
	maxHeapRise = 16 << 20
)
