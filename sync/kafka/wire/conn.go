package kafkawire

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

// The APIs this package speaks, with the range of versions it can encode.
// All are below the APIs' first flexible versions, which Kafka 3.x, 4.x
// and Redpanda all accept.
const (
	apiProduce          int16 = 0
	apiFetch            int16 = 1
	apiListOffsets      int16 = 2
	apiMetadata         int16 = 3
	apiSaslHandshake    int16 = 17
	apiApiVersions      int16 = 18
	apiCreateTopics     int16 = 19
	apiDeleteTopics     int16 = 20
	apiSaslAuthenticate int16 = 36
)

var apiRange = map[int16][2]int16{
	apiProduce:          {3, 8},
	apiFetch:            {4, 11},
	apiListOffsets:      {1, 5},
	apiMetadata:         {1, 8},
	apiSaslHandshake:    {1, 1},
	apiApiVersions:      {0, 0},
	apiCreateTopics:     {2, 4},
	apiDeleteTopics:     {1, 3},
	apiSaslAuthenticate: {1, 1},
}

var apiNames = map[int16]string{
	apiProduce: "Produce", apiFetch: "Fetch", apiListOffsets: "ListOffsets",
	apiMetadata: "Metadata", apiSaslHandshake: "SaslHandshake", apiApiVersions: "ApiVersions",
	apiCreateTopics: "CreateTopics", apiDeleteTopics: "DeleteTopics", apiSaslAuthenticate: "SaslAuthenticate",
}

// brokerConn is one TCP (or TLS) connection to a broker. Requests are
// written in turn and may be many at once; a goroutine of the connection
// reads the responses, which a broker sends in the order of the requests,
// and hands each to its caller. A broker works one request of a
// connection at a time, which is why the client keeps fetches, which wait
// on the broker, on connections of their own.
type brokerConn struct {
	addr     string
	clientID string
	nc       net.Conn
	maxResp  int

	versions map[int16][2]int16 // the broker's, from ApiVersions
	expires  time.Time          // the SASL session's end, or zero

	wmu  sync.Mutex
	corr int32

	mu      sync.Mutex
	pending []*call
	err     error // set once the connection has failed
	done    chan struct{}
}

type call struct {
	corr int32
	resp []byte
	err  error
	done chan struct{}
}

// dialBroker connects to addr, negotiates versions and authenticates.
func dialBroker(ctx context.Context, cfg *Config, addr string) (*brokerConn, error) {
	dctx, cancel := context.WithTimeout(ctx, cfg.DialTimeout)
	defer cancel()
	var nc net.Conn
	var err error
	if cfg.Dial != nil {
		nc, err = cfg.Dial(dctx, "tcp", addr)
	} else {
		nc, err = (&net.Dialer{KeepAlive: 30 * time.Second}).DialContext(dctx, "tcp", addr)
	}
	if err != nil {
		return nil, &ConnError{Addr: addr, Err: err}
	}
	if cfg.TLS != nil {
		tc := cfg.TLS.Clone()
		if tc.ServerName == "" {
			host, _, _ := net.SplitHostPort(addr)
			tc.ServerName = host
		}
		t := tls.Client(nc, tc)
		if err := t.HandshakeContext(dctx); err != nil {
			nc.Close()
			return nil, &ConnError{Addr: addr, Err: err}
		}
		nc = t
	}
	bc := &brokerConn{addr: addr, clientID: cfg.ClientID, nc: nc, maxResp: cfg.MaxResponseBytes, done: make(chan struct{})}
	go bc.readLoop()
	if err := bc.handshake(dctx, cfg); err != nil {
		bc.close(err)
		return nil, err
	}
	return bc, nil
}

// handshake reads the broker's versions and authenticates.
func (bc *brokerConn) handshake(ctx context.Context, cfg *Config) error {
	resp, err := bc.roundTrip(ctx, apiApiVersions, 0, nil, cfg.RequestTimeout)
	if err != nil {
		return err
	}
	d := decoder{b: resp}
	code := Error(d.int16())
	n := d.arrayLen()
	bc.versions = make(map[int16][2]int16, n)
	for range n {
		k := d.int16()
		bc.versions[k] = [2]int16{d.int16(), d.int16()}
	}
	if d.err != nil {
		return &ConnError{Addr: bc.addr, Err: fmt.Errorf("ApiVersions: %w", d.err)}
	}
	if code != 0 {
		return fmt.Errorf("kafka: broker %s: ApiVersions: %w", bc.addr, code)
	}
	if cfg.SASL != nil {
		return bc.authenticate(ctx, cfg)
	}
	return nil
}

// version picks the highest version of api both sides speak.
func (bc *brokerConn) version(api int16) (int16, error) {
	ours := apiRange[api]
	theirs, ok := bc.versions[api]
	v := min(ours[1], theirs[1])
	if !ok || v < ours[0] || v < theirs[0] {
		return 0, fmt.Errorf("kafka: broker %s does not speak %s %d to %d (it speaks %d to %d): %w",
			bc.addr, apiNames[api], ours[0], ours[1], theirs[0], theirs[1], ErrUnsupportedVersion)
	}
	return v, nil
}

// roundTrip sends one request and waits for its response's body (after
// the correlation ID). timeout bounds the wait: a broker silent past it
// fails the connection, and every request on it, so that a call never
// hangs on a broker that went away. A canceled ctx abandons the call; the
// response, when it comes, is dropped.
func (bc *brokerConn) roundTrip(ctx context.Context, api, version int16, body []byte, timeout time.Duration) ([]byte, error) {
	c, err := bc.send(api, version, body, true, timeout)
	if err != nil {
		return nil, err
	}
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-c.done:
		return c.resp, c.err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-t.C:
		err := &ConnError{Addr: bc.addr, Err: fmt.Errorf("no answer to %s in %v", apiNames[api], timeout)}
		bc.close(err)
		return nil, err
	}
}

// send writes a request; with expect, its call is queued for the reader.
// A Produce with no acknowledgements has no response.
func (bc *brokerConn) send(api, version int16, body []byte, expect bool, timeout time.Duration) (*call, error) {
	e := encoder{b: make([]byte, 0, 14+len(bc.clientID)+len(body))}
	e.int32(0) // size, below
	e.int16(api)
	e.int16(version)
	bc.wmu.Lock()
	defer bc.wmu.Unlock()
	bc.corr++
	e.int32(bc.corr)
	e.string(bc.clientID)
	e.b = append(e.b, body...)
	binary.BigEndian.PutUint32(e.b, uint32(len(e.b)-4))

	var c *call
	bc.mu.Lock()
	if bc.err != nil {
		err := bc.err
		bc.mu.Unlock()
		return nil, err
	}
	if expect {
		c = &call{corr: bc.corr, done: make(chan struct{})}
		bc.pending = append(bc.pending, c)
	}
	bc.mu.Unlock()
	bc.nc.SetWriteDeadline(time.Now().Add(timeout))
	if _, err := bc.nc.Write(e.b); err != nil {
		err = &ConnError{Addr: bc.addr, Err: err}
		bc.close(err)
		return nil, err
	}
	return c, nil
}

func (bc *brokerConn) readLoop() {
	r := bufio.NewReaderSize(bc.nc, 64<<10)
	var hdr [8]byte
	for {
		if _, err := io.ReadFull(r, hdr[:4]); err != nil {
			bc.close(&ConnError{Addr: bc.addr, Err: err})
			return
		}
		size := int(int32(binary.BigEndian.Uint32(hdr[:4])))
		if size < 4 || size > bc.maxResp {
			bc.close(&ConnError{Addr: bc.addr, Err: fmt.Errorf("response of %d bytes (limit %d)", size, bc.maxResp)})
			return
		}
		if _, err := io.ReadFull(r, hdr[4:8]); err != nil {
			bc.close(&ConnError{Addr: bc.addr, Err: err})
			return
		}
		corr := int32(binary.BigEndian.Uint32(hdr[4:8]))
		body := make([]byte, size-4)
		if _, err := io.ReadFull(r, body); err != nil {
			bc.close(&ConnError{Addr: bc.addr, Err: err})
			return
		}
		bc.mu.Lock()
		if len(bc.pending) == 0 || bc.pending[0].corr != corr {
			bc.mu.Unlock()
			bc.close(&ConnError{Addr: bc.addr, Err: fmt.Errorf("response to request %d out of order", corr)})
			return
		}
		c := bc.pending[0]
		bc.pending[0] = nil
		bc.pending = bc.pending[1:]
		bc.mu.Unlock()
		c.resp = body
		close(c.done)
	}
}

// close fails the connection and every request waiting on it; the first
// error stays.
func (bc *brokerConn) close(err error) {
	bc.mu.Lock()
	if bc.err != nil {
		bc.mu.Unlock()
		return
	}
	bc.err = err
	pending := bc.pending
	bc.pending = nil
	close(bc.done)
	bc.mu.Unlock()
	bc.nc.Close()
	for _, c := range pending {
		c.err = err
		close(c.done)
	}
}

// usable reports whether the connection is up and its SASL session, if
// any, has time left.
func (bc *brokerConn) usable(now time.Time) bool {
	select {
	case <-bc.done:
		return false
	default:
	}
	return bc.expires.IsZero() || now.Before(bc.expires)
}

var errClosed = errors.New("kafka: client closed")
