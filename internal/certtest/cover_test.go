package certtest

import (
	"crypto/rand"
	"crypto/x509/pkix"
	"errors"
	"testing"
	"testing/iotest"
)

// A certificate cannot be made without a serial number, which takes
// randomness: the keys are generated from the system's own, whatever
// rand.Reader is.
func TestNoRandomness(t *testing.T) {
	ca, err := NewCA("test CA")
	if err != nil {
		t.Fatal(err)
	}
	reader := rand.Reader
	rand.Reader = iotest.ErrReader(errors.New("no randomness"))
	defer func() { rand.Reader = reader }()
	if _, err := Write(t.TempDir(), "example.com"); err == nil {
		t.Error("Write made a certificate")
	}
	if _, err := ca.Issue(t.TempDir(), "", "example.com"); err == nil {
		t.Error("Issue made a certificate")
	}
}

// A CA whose key is not its certificate's issues nothing: the signature
// does not verify.
func TestIssueMismatchedKey(t *testing.T) {
	a, err := NewCA("a")
	if err != nil {
		t.Fatal(err)
	}
	b, err := NewCA("b")
	if err != nil {
		t.Fatal(err)
	}
	mixed := &CA{Cert: a.Cert, Key: b.Key}
	if _, err := mixed.IssueClient(t.TempDir(), pkix.Name{CommonName: "client"}); err == nil {
		t.Error("issued with another CA's key")
	}
}
