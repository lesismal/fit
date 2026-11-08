package mysqlwire

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"strings"
	"testing"
)

func TestNativeAndSHA256Scrambles(t *testing.T) {
	// MySQL's own: SHA1("secret") XOR SHA1(scramble + SHA1(SHA1("secret"))).
	scramble := []byte("abcdefghij0123456789")
	got := hex.EncodeToString(scrambleNative("secret", scramble))
	if len(got) != 40 || scrambleNative("", scramble) != nil || scrambleSHA256("", scramble) != nil {
		t.Fatalf("native: %s", got)
	}
	if bytes.Equal(scrambleSHA256("secret", scramble), scrambleSHA256("secret", []byte("other scramble here!"))) {
		t.Fatal("caching_sha2_password's scramble ignores the scramble")
	}
}

func TestLoginNative(t *testing.T) {
	addr := serve(t, func(f *fake) {
		f.greet("5.5.5-10.11.6-MariaDB-log", fakeCaps|capDeprecateEOF, pluginNative)
		f.response()
		if f.user != "u" || f.db != "db" || f.plugin != pluginNative ||
			!bytes.Equal(f.auth, scrambleNative("pw", f.scramble)) {
			t.Errorf("response: %q %q %q %x", f.user, f.db, f.plugin, f.auth)
		}
		if f.caps&capDeprecateEOF == 0 || f.caps&capSSL != 0 {
			t.Errorf("caps %x", f.caps)
		}
		f.ok(0, 0, StatusAutocommit|StatusInTrans)
		for {
			cmd, body := f.cmd()
			switch cmd {
			case comPing:
				f.ok(0, 0, StatusAutocommit)
			case comInitDB:
				if string(body) == "missing" {
					f.fail(1049, "42000", "Unknown database 'missing'")
				} else {
					f.ok(0, 0, StatusAutocommit)
				}
			case comQuit:
				return
			}
		}
	})
	c := dialT(t, Config{Addr: addr, User: "u", Password: "pw", Database: "db"})
	if !c.IsMariaDB() || c.ServerVersion() != "10.11.6-MariaDB-log" || c.ConnectionID() != 42 || c.TLS() {
		t.Fatalf("%v %q %d", c.IsMariaDB(), c.ServerVersion(), c.ConnectionID())
	}
	if !c.InTransaction() || c.Status()&StatusAutocommit == 0 {
		t.Fatalf("status %x", c.Status())
	}
	if err := c.Ping(ctxT(t)); err != nil || c.InTransaction() {
		t.Fatal(err)
	}
	if err := c.UseDatabase(ctxT(t), "db2"); err != nil {
		t.Fatal(err)
	}
	err := c.UseDatabase(ctxT(t), "missing")
	var se *Error
	if !errors.As(err, &se) || se.Code != 1049 || se.State != "42000" || c.Err() != nil ||
		se.Error() != "mysql: error 1049 (42000): Unknown database 'missing'" {
		t.Fatalf("%v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if err := c.Ping(ctxT(t)); !errors.Is(err, ErrClosed) {
		t.Fatalf("a ping after Close: %v", err)
	}
}

func TestLoginCachingSHA2(t *testing.T) {
	pemKey := rsaPEM(t)
	cases := []struct {
		name  string
		cfg   func(*Config)
		steps func(t *testing.T, f *fake)
		err   string
	}{
		{name: "fast path", steps: func(t *testing.T, f *fake) {
			if !bytes.Equal(f.auth, scrambleSHA256("pw", f.scramble)) {
				t.Errorf("auth %x", f.auth)
			}
			f.write([]byte{1, cachingFastOK})
			f.ok(0, 0, 0)
		}},
		{name: "full over TLS", cfg: func(c *Config) { c.TLS = clientTLS(t) }, steps: func(t *testing.T, f *fake) {
			f.write([]byte{1, cachingFullAuth})
			if p := f.read(); string(p) != "pw\x00" {
				t.Errorf("cleartext %q", p)
			}
			f.ok(0, 0, 0)
		}},
		{name: "full with the key given", cfg: func(c *Config) { c.ServerPublicKey = &testRSA(t).PublicKey }, steps: func(t *testing.T, f *fake) {
			f.write([]byte{1, cachingFullAuth})
			if pw := f.decrypt(f.read()); pw != "pw" {
				t.Errorf("password %q", pw)
			}
			f.ok(0, 0, 0)
		}},
		{name: "full with the key retrieved", cfg: func(c *Config) { c.AllowPublicKeyRetrieval = true }, steps: func(t *testing.T, f *fake) {
			f.write([]byte{1, cachingFullAuth})
			if p := f.read(); !bytes.Equal(p, []byte{cachingRequestKey}) {
				t.Errorf("request %x", p)
			}
			f.write(append([]byte{1}, pemKey...))
			if pw := f.decrypt(f.read()); pw != "pw" {
				t.Errorf("password %q", pw)
			}
			f.ok(0, 0, 0)
		}},
		{name: "full without a key", err: "needs TLS, ServerPublicKey", steps: func(t *testing.T, f *fake) {
			f.write([]byte{1, cachingFullAuth})
			f.read()
		}},
		{name: "a bad key", cfg: func(c *Config) { c.AllowPublicKeyRetrieval = true }, err: "not PEM", steps: func(t *testing.T, f *fake) {
			f.write([]byte{1, cachingFullAuth})
			f.read()
			f.write([]byte{1, 'x'})
			f.read()
		}},
		{name: "refused", err: "error 1045 (28000)", steps: func(t *testing.T, f *fake) {
			f.fail(1045, "28000", "Access denied")
		}},
		{name: "unexpected data", err: "unexpected authentication data", steps: func(t *testing.T, f *fake) {
			f.write([]byte{1, 9})
			f.read()
		}},
		{name: "unexpected answer", err: "unexpected answer 0x7", steps: func(t *testing.T, f *fake) {
			f.write([]byte{7})
			f.read()
		}},
		{name: "empty answer", err: "empty packet", steps: func(t *testing.T, f *fake) {
			f.write(nil)
			f.read()
		}},
		{name: "old password", err: "pre-4.1", steps: func(t *testing.T, f *fake) {
			f.write([]byte{0xfe})
			f.read()
		}},
		{name: "closed", err: "EOF", steps: func(t *testing.T, f *fake) {}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			addr := serve(t, func(f *fake) {
				f.greet("8.4.3", fakeCaps|capSSL|capDeprecateEOF, pluginCachingSHA2)
				f.response()
				tc.steps(t, f)
			})
			cfg := Config{Addr: addr, User: "u", Password: "pw"}
			if tc.cfg != nil {
				tc.cfg(&cfg)
			}
			c, err := Dial(ctxT(t), cfg)
			if tc.err == "" {
				if err != nil {
					t.Fatal(err)
				}
				if c.TLS() != (cfg.TLS != nil) {
					t.Fatal("TLS")
				}
				c.Close()
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Fatalf("got %v, want %q", err, tc.err)
			}
		})
	}
}

func TestLoginSwitches(t *testing.T) {
	pemKey := rsaPEM(t)
	cases := []struct {
		name   string
		plugin string // the greeting's
		cfg    func(*Config)
		steps  func(t *testing.T, f *fake)
		err    string
	}{
		{name: "to native", plugin: pluginCachingSHA2, steps: func(t *testing.T, f *fake) {
			f.scramble = []byte("ZYXWVUTSRQPONMLKJIHG")
			f.write(append(append([]byte{0xfe}, pluginNative+"\x00"...), append(f.scramble, 0)...))
			if p := f.read(); !bytes.Equal(p, scrambleNative("pw", f.scramble)) {
				t.Errorf("switched auth %x", p)
			}
			f.ok(0, 0, 0)
		}},
		{name: "unknown plugin first", plugin: "auth_gssapi_client", steps: func(t *testing.T, f *fake) {
			if f.plugin != pluginNative {
				t.Errorf("plugin %q", f.plugin)
			}
			f.ok(0, 0, 0)
		}},
		{name: "to an unknown plugin", plugin: pluginNative, err: "unknown authentication plugin", steps: func(t *testing.T, f *fake) {
			f.write([]byte("\xfeclient_ed25519\x00abcdefghij0123456789\x00"))
			f.read()
		}},
		{name: "sha256 retrieved", plugin: pluginNative, cfg: func(c *Config) { c.AllowPublicKeyRetrieval = true }, steps: func(t *testing.T, f *fake) {
			f.write([]byte("\xfesha256_password\x00abcdefghij0123456789\x00"))
			if p := f.read(); !bytes.Equal(p, []byte{sha256RequestKey}) {
				t.Errorf("request %x", p)
			}
			f.write(append([]byte{1}, pemKey...))
			if pw := f.decrypt(f.read()); pw != "pw" {
				t.Errorf("password %q", pw)
			}
			f.ok(0, 0, 0)
		}},
		{name: "sha256 first, the key given", plugin: pluginSHA256, cfg: func(c *Config) { c.ServerPublicKey = &testRSA(t).PublicKey }, steps: func(t *testing.T, f *fake) {
			if pw := f.decrypt(f.auth); pw != "pw" {
				t.Errorf("password %q", pw)
			}
			f.ok(0, 0, 0)
		}},
		{name: "sha256 over TLS", plugin: pluginSHA256, cfg: func(c *Config) { c.TLS = clientTLS(t) }, steps: func(t *testing.T, f *fake) {
			if string(f.auth) != "pw\x00" {
				t.Errorf("auth %q", f.auth)
			}
			f.ok(0, 0, 0)
		}},
		{name: "sha256 without a key", plugin: pluginNative, err: "needs ServerPublicKey", steps: func(t *testing.T, f *fake) {
			f.write([]byte("\xfesha256_password\x00abcdefghij0123456789\x00"))
			f.read()
		}},
		{name: "clear refused", plugin: pluginNative, err: "needs TLS, a Unix socket", steps: func(t *testing.T, f *fake) {
			f.write([]byte("\xfemysql_clear_password\x00"))
			f.read()
		}},
		{name: "clear allowed", plugin: pluginNative, cfg: func(c *Config) { c.AllowCleartextPassword = true }, steps: func(t *testing.T, f *fake) {
			f.write([]byte("\xfemysql_clear_password\x00"))
			if p := f.read(); string(p) != "pw\x00" {
				t.Errorf("clear %q", p)
			}
			f.ok(0, 0, 0)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			addr := serve(t, func(f *fake) {
				f.greet("8.0.40", fakeCaps|capSSL, tc.plugin)
				f.response()
				tc.steps(t, f)
			})
			cfg := Config{Addr: addr, User: "u", Password: "pw"}
			if tc.cfg != nil {
				tc.cfg(&cfg)
			}
			c, err := Dial(ctxT(t), cfg)
			if tc.err == "" {
				if err != nil {
					t.Fatal(err)
				}
				c.Close()
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Fatalf("got %v, want %q", err, tc.err)
			}
		})
	}
}

func TestLoginEmptyPasswordsAndOldClients(t *testing.T) {
	// sha256_password with no password sends one NUL; a server without
	// PLUGIN_AUTH_LENENC gets a one-byte length.
	addr := serve(t, func(f *fake) {
		f.greet("5.7.44", fakeCaps&^capPluginAuthLenEnc, pluginSHA256)
		f.response()
		if !bytes.Equal(f.auth, []byte{0}) {
			t.Errorf("auth %x", f.auth)
		}
		f.ok(0, 0, 0)
	})
	c := dialT(t, Config{Addr: addr, User: "u"})
	if c.IsMariaDB() || c.ServerVersion() != "5.7.44" {
		t.Fatal(c.ServerVersion())
	}
}

func TestGreetings(t *testing.T) {
	cases := []struct {
		name  string
		greet func(f *fake)
		cfg   Config
		err   string
	}{
		{name: "an error first", err: "error 1040: Too many connections", greet: func(f *fake) {
			f.fail(1040, "", "Too many connections")
		}},
		{name: "protocol 9", err: "protocol version 9", greet: func(f *fake) { f.write([]byte{9, 0}) }},
		{name: "short", err: "malformed packet: handshake", greet: func(f *fake) { f.write([]byte{10, 'v', 0, 1}) }},
		{name: "no protocol 41", err: "does not speak protocol 4.1", greet: func(f *fake) {
			f.greet("4.0", fakeCaps&^capProtocol41, pluginNative)
		}},
		{name: "no TLS", cfg: Config{TLS: clientTLS(t)}, err: "does not offer TLS", greet: func(f *fake) {
			f.greet("8.0", fakeCaps, pluginNative)
		}},
		{name: "TLS refused", cfg: Config{TLS: clientTLS(t)}, err: "TLS:", greet: func(f *fake) {
			f.greet("8.0", fakeCaps|capSSL, pluginNative)
			f.read()
			f.nc.Write([]byte("not a TLS record at all, no"))
		}},
		{name: "no plugin auth", greet: func(f *fake) {
			f.greet("5.1", fakeCaps&^capPluginAuth, "")
			f.response()
			f.ok(0, 0, 0)
		}},
		{name: "a malformed OK", err: "OK packet", greet: func(f *fake) {
			f.greet("8.0", fakeCaps, pluginNative)
			f.response()
			f.write([]byte{0, 0xfc})
		}},
		{name: "a malformed ERR", err: "ERR packet", greet: func(f *fake) {
			f.greet("8.0", fakeCaps, pluginNative)
			f.response()
			f.write([]byte{0xff, 1})
		}},
		{name: "a bad sha256 key", cfg: Config{AllowPublicKeyRetrieval: true}, err: "not PEM", greet: func(f *fake) {
			f.greet("8.0", fakeCaps, pluginSHA256)
			f.response()
			f.write([]byte{1, 'x'})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg
			cfg.Addr = serve(t, tc.greet)
			cfg.User, cfg.Password = "u", "pw"
			c, err := Dial(ctxT(t), cfg)
			if tc.err == "" {
				if err != nil {
					t.Fatal(err)
				}
				c.Close()
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Fatalf("got %v, want %q", err, tc.err)
			}
		})
	}
	// Nothing listening.
	if _, err := Dial(ctxT(t), Config{Addr: "127.0.0.1:1", Timeout: -1}); err == nil {
		t.Fatal("dialled port 1")
	}
}

func TestPublicKeys(t *testing.T) {
	if _, err := ParsePublicKey(rsaPEM(t)); err != nil {
		t.Fatal(err)
	}
	pk1 := pem.EncodeToMemory(&pem.Block{Type: "RSA PUBLIC KEY", Bytes: x509.MarshalPKCS1PublicKey(&testRSA(t).PublicKey)})
	if _, err := ParsePublicKey(pk1); err != nil {
		t.Fatal(err)
	}
	ec, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := x509.MarshalPKIXPublicKey(&ec.PublicKey)
	if _, err := ParsePublicKey(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})); err == nil || !strings.Contains(err.Error(), "not RSA") {
		t.Fatal(err)
	}
	if _, err := ParsePublicKey(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: []byte("junk")})); err == nil {
		t.Fatal("junk parsed")
	}
	if _, err := encryptPassword("pw", nil, &testRSA(t).PublicKey); err == nil {
		t.Fatal("no scramble")
	}
	// A password too long for the key.
	if _, err := encryptPassword(strings.Repeat("p", 300), []byte("s"), &testRSA(t).PublicKey); err == nil {
		t.Fatal("a password past the key's size")
	}
}
