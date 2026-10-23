package caddyfile

import (
	"slices"
	"strings"
	"testing"
)

// TestSizeOutOfRange checks that a request_body max_size too large for a
// number is skipped, as one that is not a size is.
func TestSizeOutOfRange(t *testing.T) {
	if _, ok := size("1" + strings.Repeat("0", 400)); ok {
		t.Error("a size out of range converted")
	}
	out, warns := adapt(t, ":8080 {\n\trequest_body {\n\t\tmax_size 1"+strings.Repeat("0", 400)+"MB\n\t}\n\trespond 200\n}\n")
	if !slices.ContainsFunc(warns, func(w string) bool { return strings.Contains(w, "request_body: skipped") }) {
		t.Errorf("warnings: %q", warns)
	}
	if len(out.Routes) != 1 || len(out.Routes[0].Middleware) != 0 {
		t.Errorf("routes: %s", js(out.Routes))
	}
}
