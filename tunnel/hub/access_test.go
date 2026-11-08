package hub

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/tunnel/internal/common"
	"github.com/lesismal/fit/tunnel/internal/transport"
)

// refusedTCP checks a connection to addr is closed as soon as it is
// accepted, with nothing read from it.
func refusedTCP(t *testing.T, what, addr string) {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	var ne net.Error
	if _, err := c.Read(make([]byte, 1)); err == nil || errors.As(err, &ne) && ne.Timeout() {
		t.Fatalf("%s %s: a refused address was served (%v)", what, addr, err)
	}
}

// servedTCP checks a connection to addr stays open.
func servedTCP(t *testing.T, what, addr string) {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("%s %s: %v", what, addr, err)
	}
	defer c.Close()
	_ = c.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	var ne net.Error
	if _, err := c.Read(make([]byte, 1)); !errors.As(err, &ne) || !ne.Timeout() {
		t.Fatalf("%s %s: an allowed address was closed: %v", what, addr, err)
	}
}

// The hub's access, then the global one, decides which addresses its
// listeners and console socket take: set on every Apply, the member's
// listeners kept, and its list files read then.
func TestAccess(t *testing.T) {
	v := newEnv(t)
	if err := v.apply(fmt.Sprintf(`{"password": "pw", "console": %s, "console_socket": {"address": "127.0.0.1:0", "password": "s"},
 "listeners": [{"protocol": "tcp", "address": "127.0.0.1:0"}, {"protocol": "quic", "address": "127.0.0.1:0"}],
 "access": {"deny": ["10.0.0.0/8"]}}`, consoleJSON("127.0.0.1:0"))); err != nil {
		t.Fatal(err)
	}
	// The certificate the quic listener made is written back first, so
	// that the members below, the current one with another access, have
	// it and write nothing back.
	if err := v.waitEdit(); err != nil {
		t.Fatal(err)
	}
	withAccess := func(access string) string {
		out, err := common.EditJSON([]byte(v.current()), func(m common.Member) error {
			m["access"] = json.RawMessage(access)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	listeners := func() (transport.Listener, transport.Listener, *socket) {
		v.s.amu.Lock()
		defer v.s.amu.Unlock()
		return v.s.listeners[0].l, v.s.listeners[1].l, v.s.socket
	}
	tcpL, quicL, sock := listeners()
	quicDial := func() error {
		c, err := transport.Dial(v.e, quicL.URL(), time.Second, transport.ClientOptions{PinSHA256: v.s.Fingerprint()},
			func(transport.Conn) transport.Handler { return nopHandler{} })
		if err == nil {
			_ = c.Close()
		}
		return err
	}
	check := func(served bool) {
		t.Helper()
		f := refusedTCP
		if served {
			f = servedTCP
		}
		f(t, "listener", tcpL.URL())
		f(t, "console_socket", v.s.SocketAddr())
		if err := quicDial(); (err == nil) != served {
			t.Fatalf("quic: served %v, dial: %v", served, err)
		}
	}
	kept := func() {
		t.Helper()
		if l0, l1, so := listeners(); l0 != tcpL || l1 != quicL || so != sock {
			t.Fatal("a change of access listened anew")
		}
	}
	// The member's deny has no say on the loopback, and no global access
	// is set: served.
	check(true)

	// The global access changes while the member does not: the same
	// member applied again takes it, its listeners kept.
	if err := v.m.Apply(&config.Config{Access: &config.Access{Deny: []string{"127.0.0.0/8"}}}); err != nil {
		t.Fatal(err)
	}
	if err := v.apply(v.current()); err != nil {
		t.Fatal(err)
	}
	kept()
	check(false)

	// The member's allow decides before the global deny.
	if err := v.apply(withAccess(`{"allow": ["127.0.0.1"]}`)); err != nil {
		t.Fatal(err)
	}
	kept()
	check(true)

	// A list file is read at Apply; one that does not read fails it, and
	// the hub goes on by the access it had.
	if err := v.apply(withAccess(`{"deny_files": ["` + filepath.Join(t.TempDir(), "none") + `"]}`)); err == nil {
		t.Fatal("applied a deny file that does not exist")
	}
	check(true)
	file := filepath.Join(t.TempDir(), "deny.txt")
	if err := os.WriteFile(file, []byte("# the loopback\n127.0.0.1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := v.m.Apply(&config.Config{}); err != nil {
		t.Fatal(err)
	}
	if err := v.apply(withAccess(`{"deny_files": ["` + file + `"]}`)); err != nil {
		t.Fatal(err)
	}
	kept()
	check(false)
	if got := certFiles(v.s.settings().cfg); len(got) == 0 || got[0] != file {
		t.Fatalf("the files watched: %v", got)
	}
}

type nopHandler struct{}

func (nopHandler) OnData([]byte) {}
func (nopHandler) OnClose(error) {}
