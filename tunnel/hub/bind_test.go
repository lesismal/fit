package hub

import (
	"errors"
	"fmt"
	"testing"

	"github.com/lesismal/fit/internal/bindtest"
)

// TestListenersRefuseHeldAddresses checks the hub's listeners, TCP and QUIC,
// and its console socket do not listen beside another server on their
// addresses, which fib's SO_REUSEPORT would let them, and each says why.
func TestListenersRefuseHeldAddresses(t *testing.T) {
	// A first Apply that fails is undone whole; the ones after it show what
	// did not open with its error.
	v := newHub(t, `{"password": "pw"}`)
	tcp, quic, ws := bindtest.Held(t, "tcp"), bindtest.Held(t, "udp"), bindtest.Held(t, "tcp")
	err := v.apply(fmt.Sprintf(`{"password": "pw",
 "listeners": [{"protocol": "tcp", "address": %q}, {"protocol": "quic", "address": %q}, {"protocol": "ws", "address": %q}]}`, tcp, quic, ws))
	bindtest.Refused(t, err, "tcp", tcp)
	st := v.s.Status()
	if len(st.Listeners) != 3 {
		t.Fatalf("status %+v", st)
	}
	for i, want := range []struct{ network, addr string }{{"tcp", tcp}, {"udp", quic}, {"tcp", ws}} {
		bindtest.Refused(t, errors.New(st.Listeners[i].Error), want.network, want.addr)
	}

	sock := bindtest.Held(t, "tcp")
	err = v.apply(fmt.Sprintf(`{"password": "pw", "console": %s, "console_socket": {"address": %q, "password": "s"}}`,
		consoleJSON("127.0.0.1:0"), sock))
	bindtest.Refused(t, err, "tcp", sock)
	if a := v.s.SocketAddr(); a != "" {
		t.Fatalf("the console socket listens on %s", a)
	}
}
