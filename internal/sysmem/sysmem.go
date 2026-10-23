// Package sysmem reads how much memory the machine fit runs on has, for the
// defaults that are a share of it.
package sysmem

import "sync"

// Total returns the bytes of memory the process may use: the machine's
// physical memory or, on Linux, its cgroup's memory limit where that is
// lower, as in a container. Zero means it could not be read. It is read once.
var Total = sync.OnceValue(total)
