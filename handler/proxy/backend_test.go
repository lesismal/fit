package proxy

import (
	"encoding/pem"
	stdhttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// upstreamProtos are the protocols the proxy tests reach their backends
// with, as config.Upstream's protocol names them.
var upstreamProtos = []string{"http/1.1", "h2c", "h2"}

// eachProto runs fn as a subtest for each upstream protocol.
func eachProto(t *testing.T, fn func(t *testing.T, proto string)) {
	for _, proto := range upstreamProtos {
		t.Run(proto, func(t *testing.T) { fn(t, proto) })
	}
}

// backendFields holds, by address, the upstream config fields that reach a
// backend serveBackend started; startFit puts them in the config.
var backendFields sync.Map

// serveBackend starts a test server speaking proto and returns its address.
func serveBackend(t testing.TB, proto string, h stdhttp.Handler) string {
	t.Helper()
	s := httptest.NewUnstartedServer(h)
	fields := ""
	switch proto {
	case "h2c":
		s.Config.Protocols = new(stdhttp.Protocols)
		s.Config.Protocols.SetHTTP1(true)
		s.Config.Protocols.SetUnencryptedHTTP2(true)
		s.Start()
		fields = `, "protocol": "h2c"`
	case "h2":
		s.EnableHTTP2 = true
		s.StartTLS()
		ca := filepath.Join(t.TempDir(), "ca.pem")
		data := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw})
		if err := os.WriteFile(ca, data, 0o644); err != nil {
			t.Fatal(err)
		}
		fields = `, "protocol": "h2", "tls_ca_file": ` + quoteJSON(ca)
	default:
		s.Start()
	}
	t.Cleanup(s.Close)
	addr := s.Listener.Addr().String()
	backendFields.Store(addr, fields)
	return addr
}

// withBackendFields puts the fields of the backend at addr into doc's
// upstream.
func withBackendFields(doc, addr string) string {
	fields, _ := backendFields.Load(addr)
	f, _ := fields.(string)
	return strings.Replace(doc, `"addresses": [%s]`, `"addresses": [%s]`+f, 1)
}

// protoOf is the protocol a request to a backend reached by proto arrives
// with.
func protoOf(proto string) string {
	if proto == "http/1.1" {
		return "HTTP/1.1"
	}
	return "HTTP/2.0"
}

func quoteJSON(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}
