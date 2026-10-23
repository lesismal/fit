//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package sysmem

import (
	"runtime"

	"golang.org/x/sys/unix"
)

func total() uint64 {
	name := "hw.physmem"
	switch runtime.GOOS {
	case "darwin":
		name = "hw.memsize"
	case "netbsd":
		name = "hw.physmem64"
	}
	n, err := unix.SysctlUint64(name)
	if err != nil {
		return 0
	}
	return n
}
