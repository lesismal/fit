//go:build !(darwin || dragonfly || freebsd || netbsd || openbsd)

package l4_test

// maxDatagram is the largest datagram TestUDPEcho sends: near what UDP over
// IPv4 can carry.
const maxDatagram = 60000
