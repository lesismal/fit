//go:build !windows

package l4_test

import "syscall"

// setRcvbuf is a net.Dialer's Control that sets the socket's receive buffer
// to n bytes before it connects, after which Linux would probe a zero window
// rather than shrink it.
func setRcvbuf(n int) func(_, _ string, rc syscall.RawConn) error {
	return func(_, _ string, rc syscall.RawConn) error {
		var serr error
		if err := rc.Control(func(fd uintptr) { serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_RCVBUF, n) }); err != nil {
			return err
		}
		return serr
	}
}
