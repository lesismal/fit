package main

import (
	"bufio"
	"flag"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMainCommand runs the command itself: two connections to a server that
// closes one of them while they are held, until its input closes.
func TestMainCommand(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	stdin, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	// The server answers both, keeps the first open, closes the second, and
	// then the command's input is closed.
	kept := make(chan net.Conn, 1)
	go func() {
		defer stdinW.Close()
		for i := range 2 {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			r := bufio.NewReader(c)
			for {
				line, err := r.ReadString('\n')
				if err != nil || line == "\r\n" {
					break
				}
			}
			_, _ = c.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 0\r\n\r\n"))
			if i == 0 {
				kept <- c
				continue
			}
			c.Close()
		}
	}()
	defer func() {
		select {
		case c := <-kept:
			c.Close()
		default:
		}
	}()

	stdout, err := os.Create(filepath.Join(t.TempDir(), "out"))
	if err != nil {
		t.Fatal(err)
	}
	defer stdout.Close()
	oldIn, oldOut, oldArgs, oldFlags := os.Stdin, os.Stdout, os.Args, flag.CommandLine
	defer func() { os.Stdin, os.Stdout, os.Args, flag.CommandLine = oldIn, oldOut, oldArgs, oldFlags }()
	os.Stdin, os.Stdout = stdin, stdout
	os.Args = []string{"idleconn", "-url", "http://" + ln.Addr().String() + "/", "-n", "2", "-concurrency", "1", "-hold", "1m"}
	flag.CommandLine = flag.NewFlagSet("idleconn", flag.ContinueOnError)
	main()
	got, err := os.ReadFile(stdout.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), "READY 2 ") || !strings.HasSuffix(string(got), "DONE 1 closed by the server\n") {
		t.Errorf("got %q", got)
	}
}

// hijackClosed is a response writer whose hijacked connection is already
// closed.
type hijackClosed struct{ *httptest.ResponseRecorder }

func (hijackClosed) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, peer := net.Pipe()
	peer.Close()
	c.Close()
	return c, bufio.NewReadWriter(bufio.NewReader(c), bufio.NewWriter(c)), nil
}

// An upgrade whose answer cannot be written returns at once.
func TestAcceptWSWriteFails(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/ws", nil)
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	acceptWS(hijackClosed{httptest.NewRecorder()}, req)
}
