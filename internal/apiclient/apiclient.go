// Package apiclient is what fit's clients of services' HTTP APIs share, in
// the control plane: Consul, etcd and the like. A client connects within a
// timeout, verifies the server with a CA of its own or the system's, may
// present a certificate, and reads answers within a bound.
package apiclient

import (
	"bytes"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

// TLSFiles are the files of a client's TLS: a CA to verify the server
// with, the system's when empty, and a certificate and key of the
// client's, none when empty.
type TLSFiles struct {
	CA, Cert, Key string
}

// Client returns an HTTP client for an API, which connects, TLS handshake
// included, within timeout, and keeps at most conns connections idle; the
// caller bounds each request with its context, as a long poll or a stream
// needs. The files are read now, so that a client made for each watch
// picks up new certificates.
func Client(files TLSFiles, timeout time.Duration, conns int) (*http.Client, error) {
	var tc *tls.Config
	if files != (TLSFiles{}) {
		tc = &tls.Config{MinVersion: tls.VersionTLS12}
		if files.CA != "" {
			pem, err := os.ReadFile(files.CA)
			if err != nil {
				return nil, err
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return nil, fmt.Errorf("%s: no PEM certificates", files.CA)
			}
			tc.RootCAs = pool
		}
		if files.Cert != "" {
			cert, err := tls.LoadX509KeyPair(files.Cert, files.Key)
			if err != nil {
				return nil, err
			}
			tc.Certificates = []tls.Certificate{cert}
		}
	}
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 15 * time.Second}
	tr := &http.Transport{
		DialContext:            dialer.DialContext,
		TLSClientConfig:        tc,
		TLSHandshakeTimeout:    timeout,
		MaxIdleConns:           conns,
		MaxIdleConnsPerHost:    conns,
		IdleConnTimeout:        30 * time.Second,
		MaxResponseHeaderBytes: 64 << 10,
		DisableCompression:     true,
	}
	return &http.Client{Transport: tr}, nil
}

// ReadBody reads a response's body whole, at most max bytes, and closes
// it; what names the request in errors.
func ReadBody(resp *http.Response, max int64, what string) ([]byte, error) {
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", what, err)
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s: the answer is larger than %d bytes", what, max)
	}
	return data, nil
}

// maxErrorBytes bounds what is read of an error's body.
const maxErrorBytes = 4 << 10

// StatusError reads a failed response's body, a little of it, closes it
// and returns an error naming what was asked and what the answer said.
func StatusError(resp *http.Response, what string) error {
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))
	resp.Body.Close()
	return fmt.Errorf("%s: %s: %s", what, resp.Status, bytes.TrimSpace(msg))
}
