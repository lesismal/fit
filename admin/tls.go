package admin

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/lesismal/fit/config"
)

// certCheckEvery is how often, at most, a handshake looks at whether the
// Admin API's certificate files have changed; a variable for tests.
var certCheckEvery = time.Second

// certFiles serves the Admin API's TLS from admin.cert_file, key_file and
// client_ca_file, read again when they change, so that a certificate a tool
// renews outside fit is served without a restart. A handshake looks at the
// files' sizes and modification times, once a second at most, on net/http's
// goroutine for the connection, never a fib worker's; files that changed
// are read, and files that do not load, as when the certificate is written
// and its key not yet, leave the running TLS as it is until they change
// again.
type certFiles struct {
	paths []string // cert, key and, with mTLS, the client CAs
	warnf func(format string, args ...any)

	mu      sync.Mutex
	config  *tls.Config
	seen    []fileStamp // the files as last tried
	checked time.Time
}

// fileStamp is what a look at a file tells without reading it.
type fileStamp struct {
	size    int64
	modTime time.Time
	ok      bool
}

// loadCertFiles loads a's certificate, key and client CAs, and fails if they
// do not load.
func loadCertFiles(a *config.Admin, warnf func(format string, args ...any)) (*certFiles, error) {
	f := &certFiles{paths: []string{a.CertFile, a.KeyFile}, warnf: warnf}
	if a.ClientCAFile != "" {
		f.paths = append(f.paths, a.ClientCAFile)
	}
	f.seen = f.look()
	c, err := f.load()
	if err != nil {
		return nil, err
	}
	f.config, f.checked = c, time.Now()
	return f, nil
}

// serverConfig returns the tls.Config the Admin API's listener is made
// with: HTTP/1.1 alone, as the API is served without TLS, and the
// certificates of the files as they are at each handshake.
func (f *certFiles) serverConfig() *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		NextProtos:         []string{"http/1.1"},
		GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) { return f.current(), nil },
	}
}

// current returns the TLS config of the files, reloading them first if
// they have changed since they were last tried.
func (f *certFiles) current() *tls.Config {
	f.mu.Lock()
	defer f.mu.Unlock()
	if now := time.Now(); now.Sub(f.checked) >= certCheckEvery {
		f.checked = now
		if s := f.look(); !slices.Equal(s, f.seen) {
			f.seen = s
			if c, err := f.load(); err != nil {
				f.warnf("%v; the running certificate stays", err)
			} else {
				f.config = c
			}
		}
	}
	return f.config
}

// load reads the files into a TLS config.
func (f *certFiles) load() (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(f.paths[0], f.paths[1])
	if err != nil {
		return nil, fmt.Errorf("admin: cert_file and key_file: %w", err)
	}
	c := &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"http/1.1"}, Certificates: []tls.Certificate{cert}}
	if len(f.paths) > 2 {
		data, err := os.ReadFile(f.paths[2])
		if err != nil {
			return nil, fmt.Errorf("admin: client_ca_file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(data) {
			return nil, fmt.Errorf("admin: client_ca_file %s: no PEM certificate found", f.paths[2])
		}
		c.ClientCAs, c.ClientAuth = pool, tls.RequireAndVerifyClientCert
	}
	return c, nil
}

// look returns the stamps of the files, in paths' order.
func (f *certFiles) look() []fileStamp {
	s := make([]fileStamp, len(f.paths))
	for i, p := range f.paths {
		if fi, err := os.Stat(p); err == nil {
			s[i] = fileStamp{size: fi.Size(), modTime: fi.ModTime(), ok: true}
		}
	}
	return s
}
