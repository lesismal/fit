//go:build darwin || dragonfly || freebsd || netbsd || openbsd

package l4_test

// maxDatagram is the largest datagram TestUDPEcho sends: the BSDs, macOS
// among them, refuse to send one past net.inet.udp.maxdgram, 9216 bytes by
// default.
const maxDatagram = 9000
