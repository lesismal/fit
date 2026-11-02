package webui

import (
	"testing"
)

func TestEvent(t *testing.T) {
	if got := string(event("end", []byte("a\r\nb"))); got != "event: end\ndata: a\ndata: b\n\n" {
		t.Fatalf("%q", got)
	}
	if got := string(event("", []byte(`{"x":1}`))); got != "data: {\"x\":1}\n\n" {
		t.Fatalf("%q", got)
	}
}
