// Command idleconn opens many idle connections to a server and holds them,
// so that the server's memory per idle connection can be measured. Each
// connection makes one request first, as a real client would, and then sits
// idle: an HTTP/1.1 keep-alive connection after its response, the same over
// TLS, or a WebSocket after its upgrade.
//
//	idleconn -mode http1 -url http://127.0.0.1:8080/idle -n 10000
//	idleconn -mode tls   -url https://127.0.0.1:8443/idle -n 10000
//	idleconn -mode ws    -url http://127.0.0.1:8080/ws -n 10000
//
// It prints "READY <n>" once every connection is open and idle, and holds
// them until its standard input closes or -hold has passed.
//
// With -serve-ws, it is instead a WebSocket backend for servers to proxy
// the ws mode to: it accepts every upgrade and holds the connection.
package main

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	stdhttp "net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

func main() {
	mode := flag.String("mode", "http1", "http1, tls or ws")
	target := flag.String("url", "", "the URL each connection requests")
	n := flag.Int("n", 10000, "how many connections to open")
	concurrency := flag.Int("concurrency", 64, "how many connections to open at once")
	hold := flag.Duration("hold", 5*time.Minute, "the longest the connections are held")
	serveWS := flag.String("serve-ws", "", "serve as a WebSocket backend on this address instead")
	flag.Parse()

	if *serveWS != "" {
		fmt.Fprintln(os.Stderr, stdhttp.ListenAndServe(*serveWS, stdhttp.HandlerFunc(acceptWS)))
		os.Exit(1)
	}
	u, err := url.Parse(*target)
	if err != nil || u.Host == "" {
		fmt.Fprintln(os.Stderr, "idleconn: -url must be an absolute URL")
		os.Exit(2)
	}
	if *mode != "http1" && *mode != "tls" && *mode != "ws" {
		fmt.Fprintln(os.Stderr, "idleconn: -mode must be http1, tls or ws")
		os.Exit(2)
	}

	start := time.Now()
	conns, err := openAll(*mode, u, *n, *concurrency)
	if err != nil {
		fmt.Fprintf(os.Stderr, "idleconn: %d of %d connections opened: %v\n", len(conns), *n, err)
		os.Exit(1)
	}
	fmt.Printf("READY %d %s\n", len(conns), time.Since(start).Round(time.Millisecond))

	stdinClosed := make(chan struct{})
	go func() {
		_, _ = io.Copy(io.Discard, os.Stdin)
		close(stdinClosed)
	}()
	select {
	case <-stdinClosed:
	case <-time.After(*hold):
	}
	// Count the connections the server closed while they were held.
	closed := 0
	for _, c := range conns {
		_ = c.SetReadDeadline(time.Now().Add(time.Millisecond))
		if _, err := c.Read(make([]byte, 1)); err != nil && !errors.Is(err, os.ErrDeadlineExceeded) {
			closed++
		}
		_ = c.Close()
	}
	fmt.Printf("DONE %d closed by the server\n", closed)
}

// openAll opens n connections, concurrency at a time, and stops at the
// first that fails.
func openAll(mode string, u *url.URL, n, concurrency int) ([]net.Conn, error) {
	conns := make([]net.Conn, n)
	var next atomic.Int64
	var mu sync.Mutex
	var firstErr error
	var wg sync.WaitGroup
	for range max(1, concurrency) {
		wg.Go(func() {
			for {
				i := int(next.Add(1) - 1)
				mu.Lock()
				stop := i >= n || firstErr != nil
				mu.Unlock()
				if stop {
					return
				}
				c, err := open(mode, u)
				mu.Lock()
				if err != nil && firstErr == nil {
					firstErr = err
				}
				conns[i] = c
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	opened := conns[:0]
	for _, c := range conns {
		if c != nil {
			opened = append(opened, c)
		}
	}
	return opened, firstErr
}

// open opens one connection and makes its request.
func open(mode string, u *url.URL) (net.Conn, error) {
	host := u.Host
	if u.Port() == "" {
		host = net.JoinHostPort(u.Hostname(), map[bool]string{false: "80", true: "443"}[mode == "tls"])
	}
	var c net.Conn
	var err error
	if mode == "tls" {
		c, err = tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", host,
			&tls.Config{InsecureSkipVerify: true, NextProtos: []string{"http/1.1"}, ServerName: u.Hostname()})
	} else {
		c, err = net.DialTimeout("tcp", host, 10*time.Second)
	}
	if err != nil {
		return nil, err
	}
	req := "GET " + u.RequestURI() + " HTTP/1.1\r\nHost: " + u.Host + "\r\n"
	want := 0 // any status
	if mode == "ws" {
		key := make([]byte, 16)
		_, _ = rand.Read(key)
		req += "Upgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: " +
			base64.StdEncoding.EncodeToString(key) + "\r\n"
		want = stdhttp.StatusSwitchingProtocols
	}
	_ = c.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.WriteString(c, req+"\r\n"); err != nil {
		c.Close()
		return nil, err
	}
	resp, err := stdhttp.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		c.Close()
		return nil, err
	}
	if want != 0 && resp.StatusCode != want {
		c.Close()
		return nil, fmt.Errorf("%s answered %s, not %d", u, resp.Status, want)
	}
	if want == 0 {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.Close {
			c.Close()
			return nil, fmt.Errorf("%s closes the connection after its response", u)
		}
	}
	_ = c.SetDeadline(time.Time{})
	return c, nil
}

// acceptWS accepts a WebSocket upgrade and holds the connection until the
// peer closes it.
func acceptWS(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	key := r.Header.Get("Sec-WebSocket-Key")
	if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") || key == "" {
		stdhttp.Error(w, "a WebSocket upgrade is expected", stdhttp.StatusBadRequest)
		return
	}
	c, rw, err := stdhttp.NewResponseController(w).Hijack()
	if err != nil {
		return
	}
	defer c.Close()
	sum := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: " +
		base64.StdEncoding.EncodeToString(sum[:]) + "\r\n\r\n")
	if rw.Flush() != nil {
		return
	}
	_, _ = io.Copy(io.Discard, rw)
}
