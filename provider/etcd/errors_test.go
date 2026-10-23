package etcd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/provider"
)

func TestKind(t *testing.T) {
	k, ok := provider.LookupKind("etcd")
	if !ok {
		t.Fatal("kind etcd is not registered")
	}
	p, watching := k("e", &config.Provider{Etcd: &config.EtcdProvider{Endpoints: []string{"http://127.0.0.1:1", "http://127.0.0.1:2"}}}, nil)
	if p.Name() != "e" || watching != `watching etcd's keys under "fit/" at http://127.0.0.1:1, http://127.0.0.1:2` {
		t.Errorf("%q, %q", p.Name(), watching)
	}
}

func TestOpenFails(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	for _, opts := range []*config.EtcdProvider{
		{Endpoints: []string{"http://127.0.0.1:1"}, CAFile: missing},
		{Endpoints: []string{"http://127.0.0.1:1"}, Username: "fit", PasswordFile: missing},
	} {
		if _, err := (&source{opts: opts}).Open(context.Background()); err == nil {
			t.Errorf("%+v: opened", opts)
		}
	}
}

func TestCallFails(t *testing.T) {
	saved := maxBodyBytes
	maxBodyBytes = 64
	t.Cleanup(func() { maxBodyBytes = saved })
	body := map[string]string{
		"/v3/kv/range":   `{"kvs": [], "more": false, "padding": "` + strings.Repeat("x", 100) + `"}`,
		"/v3/kv/other":   `nope`,
		"/v3/kv/missing": "",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := body[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(b))
	}))
	t.Cleanup(srv.Close)
	s := &source{opts: &config.EtcdProvider{Endpoints: []string{srv.URL}}}
	closeSource, err := s.Open(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer closeSource()
	// The answer is past maxBodyBytes.
	if _, _, err := s.Read(context.Background()); err == nil || !strings.Contains(err.Error(), "larger than 64 bytes") {
		t.Errorf("Read: %v", err)
	}
	var out struct{}
	if err := s.call(context.Background(), "/v3/kv/other", nil, &out); err == nil || !strings.Contains(err.Error(), "POST /v3/kv/other: invalid character") {
		t.Errorf("an answer not JSON: %v", err)
	}
	if err := s.call(context.Background(), "/v3/kv/nowhere", nil, &out); err == nil || !strings.Contains(err.Error(), "404 Not Found") {
		t.Errorf("a 404: %v", err)
	}
	s.endpoint = "::bad"
	if err := s.call(context.Background(), "/v3/kv/range", nil, &out); err == nil {
		t.Error("a bad endpoint: no error")
	}
	if p := New("e", &config.EtcdProvider{}, nil); p.Name() != "e" {
		t.Errorf("Name: %q", p.Name())
	}
}

// TestReadWatchNoResult checks that a message with neither a result nor an
// error is passed over.
func TestReadWatchNoResult(t *testing.T) {
	var ready, changed int
	err := readWatch(strings.NewReader(`{"result":{"created":true}}`+"\n"+`{}`+"\n"+`{"result":{"events":[{}]}}`+"\n"), func() { ready++ }, func() { changed++ })
	if err == nil || err.Error() != "etcd ended the watch" || ready != 1 || changed != 1 {
		t.Errorf("readWatch: %v, ready %d, changed %d", err, ready, changed)
	}
}
