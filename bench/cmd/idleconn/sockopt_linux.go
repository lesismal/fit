package main

import "syscall"

// ipBindAddressNoPort is IP_BIND_ADDRESS_NO_PORT, which the syscall package
// does not name.
const ipBindAddressNoPort = 24

// bindAddressNoPort has a socket bound to a source address take its port
// when it connects, by the four-tuple, rather than when it binds.
func bindAddressNoPort(_, _ string, rc syscall.RawConn) error {
	var serr error
	err := rc.Control(func(fd uintptr) {
		serr = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, ipBindAddressNoPort, 1)
	})
	if err != nil {
		return err
	}
	return serr
}
