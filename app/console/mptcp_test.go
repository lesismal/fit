package console

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/mptcptest"
)

// TestConsoleMPTCP: RoleConsole.mptcp opens the pages listener with
// Multipath TCP, and without it the pages' connections are plain TCP
// however the kernel offers it. The pages answer over the very connection
// whose kind is checked, so what is checked is the console's listener and
// not merely a free port. Only a kernel that offers Multipath TCP can tell:
// the test skips elsewhere.
func TestConsoleMPTCP(t *testing.T) {
	mptcptest.SkipUnlessSupported(t)
	for _, tc := range []struct {
		name  string
		mptcp bool
	}{
		{"console MPTCP on", true},
		{"console MPTCP off", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
			if err != nil {
				t.Fatal(err)
			}
			c, err := Start(newEngine(t), Options{
				Config: &config.RoleConsole{Address: "127.0.0.1:0", MPTCP: tc.mptcp,
					Users: []config.ConsoleUser{{Username: "admin", PasswordHash: string(hash)}}},
				Cookie: "fit_test",
				Pages:  fstest.MapFS{"index.html": {Data: []byte("<p>hi</p>")}},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { c.Stop(context.Background()) })

			var used *net.TCPConn
			tr := &http.Transport{DialContext: func(_ context.Context, _, address string) (net.Conn, error) {
				conn, err := mptcptest.Dial(address)
				used = conn
				return conn, err
			}}
			defer tr.CloseIdleConnections()
			resp, err := (&http.Client{Transport: tr, Timeout: 10 * time.Second}).Get(c.URL())
			if err != nil {
				t.Fatalf("GET /: %v", err)
			}
			if used == nil {
				t.Fatal("the pages' connection was not the test's")
			}
			if got := mptcptest.InUse(used); got != tc.mptcp {
				t.Fatalf("Multipath TCP in use = %v, want %v", got, tc.mptcp)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("reading the pages: %v", err)
			}
			if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), "<p>hi</p>") {
				t.Fatalf("GET /: %d %q", resp.StatusCode, body)
			}
		})
	}
}
