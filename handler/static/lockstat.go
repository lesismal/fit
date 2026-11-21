//go:build !fit_lockstat

package static

import "sync"

// The places that take a write lock of the cache, which a build with the
// fit_lockstat tag measures (see lockstat_on.go).
const (
	siteKeep = iota
	siteEvent
	siteLoad
)

// lockWait takes mu for writing, for site. It returns when it was taken in a build
// that measures the cache's locks, and 0 otherwise.
func lockWait(mu *sync.RWMutex, _ int) int64 {
	mu.Lock()
	return 0
}

// lockHold lets mu go, which lockWait took at taken, for site.
func lockHold(mu *sync.RWMutex, _ int, _ int64) {
	mu.Unlock()
}
