package syncs3

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// The examples of AWS's "Signature Calculations for the Authorization
// Header: Transferring Payload in a Single Chunk", whose signatures they
// give.
func TestSignAWSExamples(t *testing.T) {
	s := &Signer{AccessKeyID: "AKIAIOSFODNN7EXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", Region: "us-east-1", Service: "s3"}
	at := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	cases := []struct {
		name, method, path, query string
		header                    map[string]string
		payload                   string
		signed, sig               string
	}{
		{"GET Object", "GET", "/test.txt", "", map[string]string{"Range": "bytes=0-9"}, EmptySHA256,
			"host;range;x-amz-content-sha256;x-amz-date", "f0e8bdb87c964420e857bd35b5d6ed310bd44f0170aba48dd91039c6036bdb41"},
		{"PUT Object", "PUT", "/test$file.text", "", map[string]string{"Date": "Fri, 24 May 2013 00:00:00 GMT", "X-Amz-Storage-Class": "REDUCED_REDUNDANCY"},
			PayloadHash([]byte("Welcome to Amazon S3.")),
			"date;host;x-amz-content-sha256;x-amz-date;x-amz-storage-class", "98ad721746da40c64f1a55b78f14c238d841ea1380cd77a1b5971af0ece108bd"},
		{"GET Bucket Lifecycle", "GET", "/", "lifecycle", nil, EmptySHA256,
			"host;x-amz-content-sha256;x-amz-date", "fea454ca298b7da1c68078a5d1bdbfbbe0d65c699e0f91ac7a200a0136783543"},
		{"Get Bucket (List Objects)", "GET", "/", "max-keys=2&prefix=J", nil, EmptySHA256,
			"host;x-amz-content-sha256;x-amz-date", "34b48302e7b5fa45bde8084f4b7868a86f0a534bc59db6670ed5711ef69dc6f7"},
	}
	for _, c := range cases {
		u := &url.URL{Scheme: "https", Host: "examplebucket.s3.amazonaws.com", Path: c.path, RawPath: Escape(c.path, false), RawQuery: c.query}
		req := &http.Request{Method: c.method, URL: u, Host: u.Host, Header: http.Header{}}
		for k, v := range c.header {
			req.Header.Set(k, v)
		}
		s.signWith(req, c.payload, at, extraSigned(c.header))
		auth := req.Header.Get("Authorization")
		if !strings.Contains(auth, "SignedHeaders="+c.signed+",") || !strings.HasSuffix(auth, "Signature="+c.sig) {
			t.Errorf("%s: %s, want headers %s and signature %s", c.name, auth, c.signed, c.sig)
		}
	}
}

// extraSigned is the headers the examples sign besides the ones Sign does.
func extraSigned(h map[string]string) []string {
	var out []string
	for k := range h {
		if k == "Range" || k == "Date" {
			out = append(out, strings.ToLower(k))
		}
	}
	return out
}

func TestEscape(t *testing.T) {
	for in, want := range map[string]string{
		"a/b c~d":         "a/b%20c~d",
		"logs/2026+x=1&é": "logs/2026%2Bx%3D1%26%C3%A9",
	} {
		if got := Escape(in, false); got != want {
			t.Errorf("Escape(%q) = %q, want %q", in, got, want)
		}
	}
	if got := Escape("a/b", true); got != "a%2Fb" {
		t.Errorf("Escape(a/b, true) = %q", got)
	}
	if got := canonicalQuery(EncodeQuery(url.Values{"prefix": {"a b/"}, "list-type": {"2"}})); got != "list-type=2&prefix=a%20b%2F" {
		t.Errorf("canonical query %q", got)
	}
}
