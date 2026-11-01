package main

import (
	"crypto/x509"
	"encoding/pem"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// run runs the command with args, as its process would.
func run(t *testing.T, args ...string) {
	t.Helper()
	saved, savedFlags := os.Args, flag.CommandLine
	t.Cleanup(func() { os.Args, flag.CommandLine = saved, savedFlags })
	os.Args = append([]string{"certgen"}, args...)
	flag.CommandLine = flag.NewFlagSet("certgen", flag.ExitOnError)
	main()
}

func readCert(t *testing.T, dir string) *x509.Certificate {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "cert.pem"))
	if err != nil {
		t.Fatal(err)
	}
	blk, _ := pem.Decode(b)
	if blk == nil || blk.Type != "CERTIFICATE" {
		t.Fatalf("cert.pem: %q", b)
	}
	cert, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return cert
}

func TestCertgen(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "certs")
	run(t, "-dir", dir, "-hosts", " hub, 10.0.0.2,,::1 ,node.test")
	cert := readCert(t, dir)
	var ips []string
	for _, ip := range cert.IPAddresses {
		ips = append(ips, ip.String())
	}
	if !slices.Equal(cert.DNSNames, []string{"hub", "node.test"}) || !slices.Equal(ips, []string{"10.0.0.2", "::1"}) {
		t.Fatalf("names %q, addresses %q", cert.DNSNames, ips)
	}
	if err := cert.VerifyHostname("node.test"); err != nil {
		t.Fatal(err)
	}
	kb, err := os.ReadFile(filepath.Join(dir, "key.pem"))
	if err != nil {
		t.Fatal(err)
	}
	blk, _ := pem.Decode(kb)
	if blk == nil || blk.Type != "PRIVATE KEY" {
		t.Fatalf("key.pem: %q", kb)
	}
	if _, err := x509.ParsePKCS8PrivateKey(blk.Bytes); err != nil {
		t.Fatal(err)
	}

	// Both files there: kept, whatever the names asked for.
	run(t, "-dir", dir, "-hosts", "other")
	if again := readCert(t, dir); !again.Equal(cert) {
		t.Fatal("an existing certificate was replaced")
	}

	// The key missing: both are written anew.
	if err := os.Remove(filepath.Join(dir, "key.pem")); err != nil {
		t.Fatal(err)
	}
	run(t, "-dir", dir, "-hosts", "other")
	if again := readCert(t, dir); again.Equal(cert) || !slices.Equal(again.DNSNames, []string{"other"}) {
		t.Fatalf("not rewritten: %q", again.DNSNames)
	}
	if _, err := os.Stat(filepath.Join(dir, "key.pem")); err != nil {
		t.Fatal(err)
	}
}

// TestCertgenFails: a directory that cannot be made ends the command with
// an error. It runs in a process of its own, since the command exits.
func TestCertgenFails(t *testing.T) {
	if dir := os.Getenv("CERTGEN_TEST_DIR"); dir != "" {
		run(t, "-dir", dir)
		return
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestCertgenFails$")
	cmd.Env = append(os.Environ(), "CERTGEN_TEST_DIR="+filepath.Join(file, "certs"))
	out, err := cmd.CombinedOutput()
	if ee, ok := err.(*exec.ExitError); !ok || ee.Success() {
		t.Fatalf("exit: %v\n%s", err, out)
	}
}
