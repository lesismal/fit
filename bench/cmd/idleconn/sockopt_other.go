//go:build !linux

package main

import "syscall"

// bindAddressNoPort does nothing where IP_BIND_ADDRESS_NO_PORT is not had;
// several source addresses are for Linux hosts.
func bindAddressNoPort(_, _ string, _ syscall.RawConn) error { return nil }
