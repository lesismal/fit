package admin

import (
	"testing"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/mptcptest"
)

// TestAdminMPTCP: admin.mptcp opens the Admin API's listener with Multipath
// TCP, and without it the API's connections are plain TCP however the
// kernel offers Multipath TCP. Only a kernel that offers it can tell, so the
// test skips elsewhere: it checks the kernel's handshake, not a field.
func TestAdminMPTCP(t *testing.T) {
	mptcptest.SkipUnlessSupported(t)
	for _, tc := range []struct {
		name  string
		mptcp bool
	}{
		{"Admin API MPTCP on", true},
		{"Admin API MPTCP off", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := start(t, doc("one"), &config.Admin{MPTCP: tc.mptcp})
			c, err := mptcptest.Dial(f.admin.Addr().String())
			if err != nil {
				t.Fatalf("dial the Admin API: %v", err)
			}
			defer c.Close()
			if got := mptcptest.InUse(c); got != tc.mptcp {
				t.Fatalf("Multipath TCP in use = %v, want %v", got, tc.mptcp)
			}
		})
	}
}

// TestAdminConsoleMPTCP: admin.console's mptcp opens its pages listener with
// Multipath TCP too. admin starts the console from a *config.Console by
// building it a config.RoleConsole, so that conversion has to carry mptcp,
// as config.RoleConsole.Console carries it the other way.
func TestAdminConsoleMPTCP(t *testing.T) {
	mptcptest.SkipUnlessSupported(t)
	for _, tc := range []struct {
		name  string
		mptcp bool
	}{
		{"admin console MPTCP on", true},
		{"admin console MPTCP off", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := startConsole(t, &config.Console{MPTCP: tc.mptcp})
			c, err := mptcptest.Dial(f.console.Addr().String())
			if err != nil {
				t.Fatalf("dial the console's pages: %v", err)
			}
			defer c.Close()
			if got := mptcptest.InUse(c); got != tc.mptcp {
				t.Fatalf("Multipath TCP in use = %v, want %v", got, tc.mptcp)
			}
		})
	}
}
