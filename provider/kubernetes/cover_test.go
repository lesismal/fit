package kubernetes

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
)

// TestPageTooLarge: a page larger than maxPageBytes is refused, not
// decoded.
func TestPageTooLarge(t *testing.T) {
	chunk := bytes.Repeat([]byte(" "), 1<<20)
	c := plainClient(t, func(w http.ResponseWriter, r *http.Request) {
		for n := 0; n <= maxPageBytes; n += len(chunk) {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	})
	var v any
	if err := c.page(context.Background(), "/big", nil, &v); err == nil || !strings.Contains(err.Error(), "GET /big: a page is larger than") {
		t.Errorf("a page too large: %v", err)
	}
}

// TestSendBeforeSynced: nothing is sent while a kind has not been listed
// yet, even when sending is due regardless of change.
func TestSendBeforeSynced(t *testing.T) {
	var l logs
	p := New("k8s", &config.KubernetesProvider{}, l.logf)
	informers := map[string]*informer{}
	for _, k := range watched(p.opts) {
		informers[k.name] = newInformer(k, nil, 100, make(chan struct{}, 1), l.logf)
	}
	sw := &statusWriter{wake: make(chan struct{}, 1)}
	p.send(informers, func([]byte) { t.Error("a part sent before every kind was listed") }, sw, true)
}
