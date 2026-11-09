//go:build !windows

package btstream

import "syscall"

func setRcvbuf(fd uintptr, n int) error {
	return syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, n)
}
