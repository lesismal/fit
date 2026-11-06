package listener

import (
	"net"
	"strings"
	"testing"

	"github.com/lesismal/fit/runtime"
)

// TestPlan: Plan says which listeners a reload would bind and remove, tries
// the new addresses as bind does and binds nothing.
func TestPlan(t *testing.T) {
	_, s := start(t, reloadDoc("a", "web 127.0.0.1:0 http/1.1", "old 127.0.0.1:0 http/1.1 h2c"))
	before := s.Addrs()

	g := compile(t, reloadDoc("a", "web 127.0.0.1:0 http/1.1", "new 127.0.0.1:0 http/1.1"))
	added, removed, err := s.Plan(g)
	g.Release()
	if err != nil || len(added) != 1 || added[0].Name != "new" || added[0].Addr.String() != "127.0.0.1:0" || added[0].Addr.Network() != "tcp" ||
		len(removed) != 1 || removed[0].Name != "old" {
		t.Fatalf("Plan: %v %v %v", added, removed, err)
	}
	if after := s.Addrs(); len(after) != len(before) || after[0].Addr.String() != before[0].Addr.String() || after[1].Addr.String() != before[1].Addr.String() {
		t.Errorf("Plan changed the listeners: %v, was %v", after, before)
	}

	// An address another socket holds: refused as bind refuses it.
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	g = compile(t, reloadDoc("a", "web 127.0.0.1:0 http/1.1", "new "+taken.Addr().String()+" http/1.1"))
	_, _, err = s.Plan(g)
	g.Release()
	if err == nil || !strings.Contains(err.Error(), `listener "new"`) {
		t.Errorf("a taken address: %v", err)
	}

	// A UDP listener is planned as one.
	g = &runtime.Graph{Listeners: []*runtime.Listener{{Name: "u", Address: "127.0.0.1:0", Protocols: []string{"udp"}}}}
	if added, _, err := s.Plan(g); err != nil || len(added) != 1 || added[0].Addr.Network() != "udp" {
		t.Errorf("a UDP listener: %v %v", added, err)
	}
}

// TestTryBind: the addresses a listener would bind are tried for TCP, UDP or
// both, as its protocols say.
func TestTryBind(t *testing.T) {
	tcp, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer tcp.Close()
	udp, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer udp.Close()
	for _, c := range []struct {
		address   string
		protocols []string
		fails     bool
	}{
		{tcp.Addr().String(), []string{"tcp"}, true},
		{udp.LocalAddr().String(), []string{"udp"}, true},
		{udp.LocalAddr().String(), []string{"h3"}, true},
		{tcp.Addr().String(), []string{"http/1.1", "h3"}, true},
		{"127.0.0.1:0", []string{"http/1.1", "h3"}, false},
		{"127.0.0.1:0", []string{"tcp"}, false},
		{"127.0.0.1:0", []string{"udp"}, false},
	} {
		err := tryBind(&runtime.Listener{Name: "x", Address: c.address, Protocols: c.protocols})
		if (err != nil) != c.fails {
			t.Errorf("%s %v: %v", c.address, c.protocols, err)
		}
	}
}
