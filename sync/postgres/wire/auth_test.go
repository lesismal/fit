package pgwire

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"strings"
	"testing"
	"time"
)

// SCRAM against the fake, over TLS or not, with each channel binding mode
// and the mechanisms the server offers.
func TestSCRAMModes(t *testing.T) {
	ecCert, ecRoots, ecLeaf := testCert(t, false)
	edCert, edRoots, _ := testCert(t, true)
	ecCB := sha256.Sum256(ecLeaf.Raw)
	both := []string{"SCRAM-SHA-256-PLUS", "SCRAM-SHA-256"}
	plain := []string{"SCRAM-SHA-256"}
	for _, c := range []struct {
		name    string
		cert    *tls.Certificate
		roots   *x509.CertPool
		mechs   []string
		ssl, cb string
		gs2     string
		bound   bool
		err     string
	}{
		{"plus", ecCert, ecRoots, both, "", "", "p=tls-server-end-point,,", true, ""},
		{"plus required", ecCert, ecRoots, both, "", ChannelBindingRequire, "p=tls-server-end-point,,", true, ""},
		{"plus disabled", ecCert, ecRoots, both, "", ChannelBindingDisable, "n,,", false, ""},
		{"server cannot bind", ecCert, ecRoots, plain, "", "", "y,,", false, ""},
		{"no TLS", nil, nil, both, SSLDisable, "", "n,,", false, ""},
		{"ed25519 prefer", edCert, edRoots, both, "", "", "n,,", false, ""},
		{"ed25519 require", edCert, edRoots, both, "", ChannelBindingRequire, "", false, "no hash"},
		{"required, server cannot", ecCert, ecRoots, plain, "", ChannelBindingRequire, "", false, "channel binding is required"},
		{"no mechanism", ecCert, ecRoots, []string{"OTHER"}, "", "", "", false, "no SASL mechanism"},
		{"only plus, no TLS", nil, nil, []string{"SCRAM-SHA-256-PLUS"}, SSLDisable, "", "", false, "no SASL mechanism"},
	} {
		t.Run(c.name, func(t *testing.T) {
			gs2 := make(chan string, 1)
			addr := fakeServerOpts(t, fakeOpts{cert: c.cert}, func(f *fakeConn) {
				f.startup()
				gs2 <- f.scram("secret", c.mechs, ecCB[:])
				f.until('X')
			})
			conn, err := Connect(ctx(t), Config{Addr: addr, User: "u", Password: "secret", SSLMode: c.ssl,
				ChannelBinding: c.cb, TLS: &tls.Config{RootCAs: c.roots}})
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("err %v, want %q", err, c.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			if got := <-gs2; got != c.gs2 || conn.ChannelBound() != c.bound || (conn.TLS() != nil) != (c.cert != nil) {
				t.Fatalf("gs2 %q bound %v TLS %v", got, conn.ChannelBound(), conn.TLS() != nil)
			}
		})
	}

	// A wrong password is the server's error.
	addr := fakeServer(t, func(f *fakeConn) {
		f.startup()
		f.scram("secret", plain, nil)
	})
	var pe *Error
	if _, err := Connect(ctx(t), Config{Addr: addr, User: "u", Password: "wrong", SSLMode: SSLDisable}); !errors.As(err, &pe) || pe.Code != "28P01" {
		t.Fatalf("err %v", err)
	}
}

func TestTLSModes(t *testing.T) {
	cert, roots, _ := testCert(t, false)
	trust := func(f *fakeConn) {
		f.startup()
		f.ready()
		f.until('X')
	}
	addr := fakeServerOpts(t, fakeOpts{cert: cert}, trust)
	for _, c := range []struct {
		mode, name string
		roots      *x509.CertPool
		ok         bool
	}{
		{"", "", roots, true},
		{SSLVerifyFull, "wrong.example", roots, false},
		{SSLVerifyFull, "", x509.NewCertPool(), false},
		{SSLVerifyCA, "wrong.example", roots, true},
		{SSLVerifyCA, "", x509.NewCertPool(), false},
		{SSLRequire, "wrong.example", x509.NewCertPool(), true},
	} {
		conn, err := Connect(ctx(t), Config{Addr: addr, User: "u", SSLMode: c.mode, TLS: &tls.Config{RootCAs: c.roots, ServerName: c.name}})
		if (err == nil) != c.ok {
			t.Fatalf("%+v: %v", c, err)
		}
		if err == nil {
			conn.Close()
		}
	}
	// With no TLS config the system roots are used, which do not know the
	// fake's certificate.
	if _, err := Connect(ctx(t), Config{Addr: addr, User: "u"}); err == nil || !strings.Contains(err.Error(), "TLS") {
		t.Fatalf("err %v", err)
	}
}

// The authentication requests other than SCRAM, and malformed exchanges.
func TestAuthRequests(t *testing.T) {
	for _, c := range []struct {
		name   string
		cfg    Config
		script func(f *fakeConn)
		err    string
	}{
		{"md5 refused", Config{}, func(f *fakeConn) { f.send('R', u32(5), []byte{1, 2, 3, 4}) }, "MD5"},
		{"md5 short", Config{AllowMD5: true}, func(f *fakeConn) { f.send('R', u32(5), []byte{1}) }, "malformed"},
		{"cleartext refused", Config{}, func(f *fakeConn) { f.send('R', u32(3)) }, "cleartext"},
		{"cleartext", Config{AllowCleartext: true, Password: "pw"}, func(f *fakeConn) {
			f.send('R', u32(3))
			if _, b := f.read(); string(b) != "pw\x00" {
				f.t.Errorf("password %q", b)
			}
			f.ready()
		}, ""},
		{"trust with binding required", Config{ChannelBinding: ChannelBindingRequire}, func(f *fakeConn) { f.ready() }, "channel binding"},
		{"kerberos", Config{}, func(f *fakeConn) { f.send('R', u32(7)) }, "method 7"},
		{"error", Config{}, func(f *fakeConn) { f.errResp("28000", "no pg_hba.conf entry") }, "pg_hba"},
		{"not R", Config{}, func(f *fakeConn) { f.send('Z', []byte{'I'}) }, "unexpected"},
		{"short R", Config{}, func(f *fakeConn) { f.send('R', []byte{0}) }, "unexpected"},
		{"negotiate version", Config{}, func(f *fakeConn) {
			f.send('v', u32(196608), u32(0))
			f.ready()
		}, ""},
		{"continue first", Config{}, func(f *fakeConn) { f.send('R', u32(11), []byte("r=x")) }, "unexpected"},
		{"final first", Config{}, func(f *fakeConn) { f.send('R', u32(12), []byte("v=x")) }, "unexpected"},
		{"bad mechanisms", Config{}, func(f *fakeConn) { f.send('R', u32(10), []byte("SCRAM")) }, "malformed"},
		{"ok before the proof", Config{}, func(f *fakeConn) {
			f.send('R', u32(10), cs("SCRAM-SHA-256"), []byte{0})
			f.read()
			f.ready()
		}, "without proving"},
		{"bad server first", Config{}, func(f *fakeConn) {
			f.send('R', u32(10), cs("SCRAM-SHA-256"), []byte{0})
			f.read()
			f.send('R', u32(11), []byte("e=other-error"))
		}, "other-error"},
		{"bad server final", Config{}, func(f *fakeConn) {
			f.send('R', u32(10), cs("SCRAM-SHA-256"), []byte{0})
			_, b := f.read()
			nonce := string(b[strings.Index(string(b), "r=")+2:])
			f.send('R', u32(11), []byte("r="+nonce+"x,s=c2FsdA==,i=1"))
			f.read()
			f.send('R', u32(12), []byte("e=invalid-proof"))
		}, "invalid-proof"},
		{"key data short", Config{}, func(f *fakeConn) {
			f.send('R', u32(0))
			f.send('K', u32(1))
		}, "malformed"},
		{"error after ok", Config{}, func(f *fakeConn) {
			f.send('R', u32(0))
			f.errResp("53300", "too many connections")
		}, "too many"},
		{"unexpected after ok", Config{}, func(f *fakeConn) {
			f.send('R', u32(0))
			f.send('D')
		}, "unexpected"},
	} {
		t.Run(c.name, func(t *testing.T) {
			addr := fakeServer(t, func(f *fakeConn) {
				f.startup()
				c.script(f)
				f.until('X')
			})
			cfg := c.cfg
			cfg.Addr, cfg.User, cfg.SSLMode = addr, "u", SSLDisable
			if c.name == "trust with binding required" {
				cfg.SSLMode = SSLRequire
				cert, _, _ := testCert(t, false)
				addr = fakeServerOpts(t, fakeOpts{cert: cert}, func(f *fakeConn) {
					f.startup()
					c.script(f)
				})
				cfg.Addr = addr
			}
			conn, err := Connect(ctx(t), cfg)
			if c.err == "" {
				if err != nil {
					t.Fatal(err)
				}
				conn.Close()
				return
			}
			if err == nil || !strings.Contains(err.Error(), c.err) {
				t.Fatalf("err %v, want %q", err, c.err)
			}
		})
	}
}

func TestSCRAMMessages(t *testing.T) {
	s := &scram{password: "p", gs2: "n,,"}
	s.clientFirst("", "abc")
	for _, m := range []string{"r=abcd,s=!!,i=1", "r=abcd,s=c2FsdA==,i=0"} {
		if _, err := s.serverFirst([]byte(m)); err == nil {
			t.Errorf("%q accepted", m)
		}
	}
	for _, m := range []string{"x=1", "v=!!", "v=AAAA,ext"} {
		if err := s.serverFinal([]byte(m)); err == nil {
			t.Errorf("%q accepted", m)
		}
	}
	for alg, ok := range map[x509.SignatureAlgorithm]bool{
		x509.SHA256WithRSA: true, x509.SHA384WithRSA: true, x509.SHA512WithRSA: true, x509.PureEd25519: false,
	} {
		cb, err := tlsServerEndPoint(&x509.Certificate{SignatureAlgorithm: alg, Raw: []byte("cert")})
		if (err == nil) != ok || (ok && len(cb) < 32) {
			t.Errorf("%v: %d bytes, %v", alg, len(cb), err)
		}
	}
}

func TestConnectConfig(t *testing.T) {
	for _, cfg := range []Config{
		{SSLMode: "prefer"},
		{ChannelBinding: "maybe"},
		{SSLMode: SSLDisable, ChannelBinding: ChannelBindingRequire},
	} {
		if _, err := Connect(ctx(t), cfg); err == nil {
			t.Errorf("%+v accepted", cfg)
		}
	}
	// A host without a port gets 5432; a dial that cannot start fails.
	done, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Connect(done, Config{Addr: "127.0.0.1", SSLMode: SSLDisable}); err == nil {
		t.Fatal("a cancelled dial connected")
	}

	// The startup parameters.
	got := make(chan map[string]string, 1)
	addr := fakeServer(t, func(f *fakeConn) {
		got <- f.startup()
		f.ready()
		f.until('X')
	})
	c, err := Connect(context.Background(), Config{Addr: addr, User: "u", Database: "db", SSLMode: SSLDisable, Replication: true,
		Params: map[string]string{"search_path": "s", "user": "ignored", "TimeZone": "UTC"}})
	if err != nil {
		t.Fatal(err)
	}
	p := <-got
	if p["user"] != "u" || p["database"] != "db" || p["replication"] != "database" || p["search_path"] != "s" ||
		p["client_encoding"] != "UTF8" || p["application_name"] != "fit" || p["TimeZone"] != "UTC" {
		t.Fatalf("%v", p)
	}
	if c.TLS() != nil || c.TxStatus() != 'I' || c.ParameterStatus("server_version") == "" || c.Err() != nil {
		t.Fatal("state after startup")
	}
	c.Close()
	c.Close()
	if !errors.Is(c.Err(), ErrClosed) {
		t.Fatalf("%v", c.Err())
	}

	// A server that never answers: the connect timeout ends it.
	addr = fakeServer(t, func(f *fakeConn) {
		f.startup()
		f.read()
	})
	if _, err := Connect(context.Background(), Config{Addr: addr, SSLMode: SSLDisable, ConnectTimeout: 50 * time.Millisecond}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
	// A server that closes at once, with and without TLS asked for.
	addr = fakeServer(t, func(f *fakeConn) {})
	for _, mode := range []string{SSLDisable, SSLRequire} {
		if _, err := Connect(ctx(t), Config{Addr: addr, SSLMode: mode}); err == nil {
			t.Fatalf("%s: connected to a closed server", mode)
		}
	}
}

func TestServerVersion(t *testing.T) {
	c := &Conn{params: map[string]string{}}
	for v, want := range map[string]int{"17.2 (Debian 17.2-1)": 170002, "13.18": 130018, "18beta1": 180000, "9.6.3": 0, "x": 0} {
		c.params["server_version"] = v
		if got := c.ServerVersion(); got != want {
			t.Errorf("%q: %d, want %d", v, got, want)
		}
	}
}
