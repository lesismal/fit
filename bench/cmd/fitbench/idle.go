package main

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The kinds of idle connection: HTTP/1.1 keep-alive and TLS after a request
// the server answers 404, and a WebSocket the server proxies to the
// WebSocket backend.
var idleKinds = []struct{ name, mode, url string }{
	{"http1", "http1", "http://%s:8080/idle"},
	{"tls", "tls", "https://%s:8443/idle"},
	{"ws", "ws", "http://%s:8080/ws"},
}

// IdleResult is the memory a server holds for idle connections of one kind.
type IdleResult struct {
	Server      string `json:"server"`
	Kind        string `json:"kind"`
	Connections int    `json:"connections,omitempty"`
	Skipped     string `json:"skipped,omitempty"`
	Error       string `json:"error,omitempty"`
	// AnonBefore and AnonAfter are the server's anonymous memory (what its
	// processes hold, without page cache) before the connections were
	// opened and once they had sat idle for the settle time; MemoryBefore
	// and MemoryAfter its whole cgroup memory.
	AnonBefore   int64 `json:"anon_before"`
	AnonAfter    int64 `json:"anon_after"`
	MemoryBefore int64 `json:"memory_before"`
	MemoryAfter  int64 `json:"memory_after"`
	// BytesPerConn is (AnonAfter - AnonBefore) / Connections.
	BytesPerConn float64 `json:"bytes_per_conn"`
	// ClosedByServer counts connections the server closed while they were
	// held, which make BytesPerConn an underestimate.
	ClosedByServer int `json:"closed_by_server"`
	// GoroutinesBefore and GoroutinesAfter are a Go server's goroutines at
	// the same two moments, for a server with an Admin API.
	GoroutinesBefore int `json:"goroutines_before,omitempty"`
	GoroutinesAfter  int `json:"goroutines_after,omitempty"`
}

func idleKindNames() []string {
	var names []string
	for _, k := range idleKinds {
		names = append(names, k.name)
	}
	return names
}

// runIdle measures s's memory for each chosen kind of idle connection, each
// on a server freshly started for it.
func (h *harness) runIdle(ctx context.Context, s server) {
	for _, kind := range h.idle {
		if ctx.Err() != nil {
			return
		}
		r := IdleResult{Server: s.name, Kind: kind}
		if !slices.Contains(s.idle, kind) {
			r.Skipped = s.name + " cannot serve it"
		} else {
			h.measureIdle(ctx, s, kind, &r)
		}
		h.report.Idle = append(h.report.Idle, r)
		printIdle(r)
	}
}

func (h *harness) measureIdle(ctx context.Context, s server, kind string, r *IdleResult) {
	defer func() { _, _ = docker(context.Background(), "rm", "-f", serverName) }()
	if err := h.start(ctx, s, serverName, h.serverCPUs); err != nil {
		r.Error = err.Error()
		return
	}
	if err := h.waitReady(ctx, serverName, "http://"+h.serverHost+":8080"+s.ready); err != nil {
		r.Error = err.Error()
		return
	}
	time.Sleep(time.Second) // let the server settle after starting
	r.AnonBefore, r.MemoryBefore = h.serverMemory(ctx)
	if h.canCount(s) {
		r.GoroutinesBefore, _ = h.goroutines(ctx, s)
	}

	var u, mode string
	for _, k := range idleKinds {
		if k.name == kind {
			u, mode = fmt.Sprintf(k.url, h.serverHost), k.mode
		}
	}
	sources := h.sources(h.idleConns)
	cmd := exec.CommandContext(ctx, "docker", "exec", "-i", loadgenName, "/fitbench/idleconn",
		"-mode", mode, "-url", u, "-n", strconv.Itoa(h.idleConns), "-hold", "5m", "-sources", strconv.Itoa(sources))
	stdin, err := cmd.StdinPipe()
	if err != nil {
		r.Error = err.Error()
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		r.Error = err.Error()
		return
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		r.Error = err.Error()
		return
	}
	lines := bufio.NewScanner(stdout)
	ready := false
	for lines.Scan() {
		if f := strings.Fields(lines.Text()); len(f) >= 2 && f[0] == "READY" {
			r.Connections, _ = strconv.Atoi(f[1])
			ready = true
			break
		}
	}
	if ready {
		time.Sleep(h.idleSettle)
		r.AnonAfter, r.MemoryAfter = h.serverMemory(ctx)
		if h.canCount(s) {
			r.GoroutinesAfter, _ = h.goroutines(ctx, s)
		}
	}
	stdin.Close()
	for lines.Scan() {
		if f := strings.Fields(lines.Text()); len(f) >= 2 && f[0] == "DONE" {
			r.ClosedByServer, _ = strconv.Atoi(f[1])
		}
	}
	waitErr := cmd.Wait()
	switch {
	case !ready:
		r.Error = fmt.Sprintf("idleconn: %v: %s", waitErr, strings.TrimSpace(stderr.String()))
	case r.Connections > 0:
		r.BytesPerConn = float64(r.AnonAfter-r.AnonBefore) / float64(r.Connections)
	}
}

// serverMemory reads the server container's anonymous and whole cgroup
// memory.
func (h *harness) serverMemory(ctx context.Context) (anon, total int64) {
	if out, err := docker(ctx, "exec", serverName, "cat", "/sys/fs/cgroup/memory.stat"); err == nil {
		anon, _ = cgroupAnon(out)
	}
	if out, err := docker(ctx, "exec", serverName, "cat", "/sys/fs/cgroup/memory.current"); err == nil {
		total, _ = strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	}
	return anon, total
}

func printIdle(r IdleResult) {
	name := fmt.Sprintf("%-8s idle-%-6s", r.Server, r.Kind)
	switch {
	case r.Skipped != "":
		fmt.Printf("    %s skipped: %s\n", name, r.Skipped)
	case r.Error != "":
		fmt.Printf("    %s error: %s\n", name, r.Error)
	default:
		fmt.Printf("    %s %6d connections  %8.0f bytes each  (anon %d -> %d)  closed by server %d\n",
			name, r.Connections, r.BytesPerConn, r.AnonBefore, r.AnonAfter, r.ClosedByServer)
	}
}

// writeCert writes a self-signed ECDSA P-256 certificate and its key, for
// the servers' TLS listeners, as cert.pem and key.pem in dir.
func writeCert(dir string) error {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return err
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "fitbench"},
		DNSNames:     []string{"localhost", serverName},
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "cert.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return err
	}
	// Readable by the servers' unprivileged users; the key is throwaway.
	return os.WriteFile(filepath.Join(dir, "key.pem"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o644)
}

// sources is how many loopback addresses n connections to the server are
// opened from: one has the local port range to connect from, and every four
// fifths of it (40k when the range is not known) take another, which host
// networking reaches on loopback.
func (h *harness) sources(n int) int {
	if h.serverHost != "127.0.0.1" {
		return 1
	}
	per := 40000
	if ports := h.machine.ports(); ports > 0 {
		per = max(1, ports*8/10)
	}
	return max(1, (n+per-1)/per)
}
