// Command certgen writes the self-signed certificate of the docker
// end-to-end test of the LLM feature: cert.pem and key.pem in -dir, for the names
// in -hosts. The roles serve their pages and the proxy its API over TLS
// with it, since they listen on addresses that are not loopback; the tester
// does not verify it. Files that exist already are kept.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"log"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	dir := flag.String("dir", "/certs", "the directory to write cert.pem and key.pem in")
	hosts := flag.String("hosts", "localhost,127.0.0.1", "the certificate's names and addresses, comma separated")
	flag.Parse()
	certPath, keyPath := filepath.Join(*dir, "cert.pem"), filepath.Join(*dir, "key.pem")
	if _, err := os.Stat(certPath); err == nil {
		if _, err := os.Stat(keyPath); err == nil {
			log.Printf("certgen: %s and %s exist; kept", certPath, keyPath)
			return
		}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		log.Fatal(err)
	}
	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 62))
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "fit llm e2e"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	for _, h := range strings.Split(*hosts, ",") {
		if h = strings.TrimSpace(h); h == "" {
			continue
		}
		if ip := net.ParseIP(h); ip != nil {
			tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
		} else {
			tmpl.DNSNames = append(tmpl.DNSNames, h)
		}
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		log.Fatal(err)
	}
	kb, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		log.Fatal(err)
	}
	if err := os.MkdirAll(*dir, 0o755); err != nil {
		log.Fatal(err)
	}
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		log.Fatal(err)
	}
	// Readable by every user of the containers: the key is a throwaway, and
	// the roles need not run as the user that wrote it.
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kb}), 0o644); err != nil {
		log.Fatal(err)
	}
	log.Printf("certgen: wrote %s and %s for %s", certPath, keyPath, *hosts)
}
