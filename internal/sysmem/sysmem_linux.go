package sysmem

import (
	"os"
	"strconv"
	"strings"
	"syscall"
)

// cgroupLimits are where the cgroup's memory limit is read, v2's and then
// v1's; v1 writes a huge number for no limit, which the machine's memory
// is below anyway.
var cgroupLimits = []string{"/sys/fs/cgroup/memory.max", "/sys/fs/cgroup/memory/memory.limit_in_bytes"}

func total() uint64 {
	var info syscall.Sysinfo_t
	if syscall.Sysinfo(&info) != nil {
		return 0
	}
	n := uint64(info.Totalram) * uint64(info.Unit)
	for _, path := range cgroupLimits {
		if limit := readLimit(path); limit > 0 && (n == 0 || limit < n) {
			n = limit
		}
	}
	return n
}

// readLimit reads a cgroup memory limit file: zero for none, or for "max".
func readLimit(path string) uint64 {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return 0
	}
	return n
}
