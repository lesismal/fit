package static

import (
	goruntime "runtime"
	"sync"
	"time"

	"github.com/lesismal/fit/internal/filepool"
)

// The file pool's defaults.
const (
	// DefaultFileTimeout bounds how long a request waits for the file pool:
	// to be taken up, and then for its file to be found and opened.
	DefaultFileTimeout = 10 * time.Second
	// fileQueue bounds the requests waiting for a worker of the pool.
	fileQueue = 4096
)

var (
	sharedPoolOnce sync.Once
	sharedPool     *filepool.Pool
)

// defaultPool is the pool every static handler shares for its file
// operations, stat, open, read and list: 4 workers a CPU, and at least 16,
// which is about what nginx's thread pools and Go's own os package make of a
// disk.
func defaultPool() *filepool.Pool {
	sharedPoolOnce.Do(func() {
		sharedPool = filepool.New(max(16, 4*goruntime.GOMAXPROCS(0)), fileQueue, DefaultFileTimeout)
	})
	return sharedPool
}
