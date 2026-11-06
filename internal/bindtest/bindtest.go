// Package bindtest holds an address the way another fib server would, for
// the tests that check fit's bindings refuse to share it.
package bindtest

import (
	"strings"
	"testing"

	"github.com/lesismal/fib"
)

// Held returns a loopback address on network, "tcp" or "udp", that an engine
// of its own listens on with fib's defaults, SO_REUSEPORT among them, until
// the test ends: a second fib binding there would listen beside it without an
// error, and the kernel would split the connections between the two, but for
// package bindcheck.
func Held(t testing.TB, network string) string {
	t.Helper()
	e, err := fib.New(fib.DefaultConfig(), fib.Binding{Network: network, Addr: "127.0.0.1:0", Handler: &fib.HandlerFuncs{}})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { _ = e.Run(); close(done) }()
	t.Cleanup(func() { e.Stop(); <-done; _ = e.Close() })
	addrs, err := e.BindingAddrs(0)
	if err != nil || len(addrs) == 0 {
		t.Fatalf("BindingAddrs: %v %v", addrs, err)
	}
	return addrs[0].String()
}

// Refused fails the test unless err is bindcheck's refusal of address on
// network: in use, with the transport and the address named.
func Refused(t testing.TB, err error, network, address string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), strings.ToUpper(network)+" "+address+" is taken") {
		t.Fatalf("binding %s %s, which another server holds: %v, want it refused as taken", network, address, err)
	}
}
