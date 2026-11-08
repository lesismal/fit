package kafkawire

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/tls"
	"encoding/base64"
	"encoding/binary"
	"hash"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeCluster is brokers in the test's process that speak the APIs the
// client does, at the versions it picks, keeping each partition's batches
// in memory. A hook can answer any request instead, drop it, or close the
// connection, which is how the tests make leaders move and brokers fail.
type fakeCluster struct {
	t *testing.T

	mu         sync.Mutex
	brokers    []*fakeBroker
	topics     map[string][]*fakePart
	controller int32
	versions   map[int16][2]int16 // overrides of what the brokers speak
	users      map[string]string  // SASL users; nil: no SASL
	lifetime   int64              // SASL session lifetime in ms
	hook       func(b *fakeBroker, api, version int16, body []byte) (fakeAction, []byte)
	requests   map[int16]int
}

type fakeAction int

const (
	fakeNormal    fakeAction = iota
	fakeReply                // answer with the body the hook returns
	fakeDrop                 // never answer
	fakeClose                // close the connection
	fakeBadCorr              // answer with the wrong correlation ID
	fakeCutHeader            // answer with a size and half a correlation ID, then close
	fakeCutBody              // answer with a size, a correlation ID and half the body, then close
)

type fakePart struct {
	leader  int32
	batches [][]byte
	next    int64
	aborted []abortedTxn
	err     Error // the partition's error in metadata
}

type fakeBroker struct {
	c    *fakeCluster
	id   int32
	ln   net.Listener
	host string
	port int32
	down bool // refuses connections, as a stopped broker
}

func (b *fakeBroker) addr() string { return net.JoinHostPort(b.host, strconv.Itoa(int(b.port))) }

func newFakeCluster(t *testing.T, n int, tlsConf *tls.Config) *fakeCluster {
	t.Helper()
	c := &fakeCluster{t: t, topics: map[string][]*fakePart{}, controller: 1, requests: map[int16]int{}}
	for i := range n {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		if tlsConf != nil {
			ln = tls.NewListener(ln, tlsConf)
		}
		a := ln.Addr().(*net.TCPAddr)
		b := &fakeBroker{c: c, id: int32(i + 1), ln: ln, host: "127.0.0.1", port: int32(a.Port)}
		c.brokers = append(c.brokers, b)
		go b.serve()
	}
	t.Cleanup(func() {
		for _, b := range c.brokers {
			b.ln.Close()
		}
	})
	return c
}

func (c *fakeCluster) addrs() []string {
	var s []string
	for _, b := range c.brokers {
		s = append(s, b.addr())
	}
	return s
}

// addTopic makes a topic whose partitions' leaders go round the brokers.
func (c *fakeCluster) addTopic(name string, partitions int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range partitions {
		c.topics[name] = append(c.topics[name], &fakePart{leader: int32(i%len(c.brokers) + 1)})
	}
}

// set changes the cluster under its lock.
func (c *fakeCluster) set(f func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f()
}

func (c *fakeCluster) setHook(h func(b *fakeBroker, api, version int16, body []byte) (fakeAction, []byte)) {
	c.set(func() { c.hook = h })
}

func (c *fakeCluster) setLeader(topic string, p int, leader int32) {
	c.mu.Lock()
	c.topics[topic][p].leader = leader
	c.mu.Unlock()
}

func (c *fakeCluster) count(api int16) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.requests[api]
}

// appendRaw stores a batch as it is, at the partition's next offset.
func (c *fakeCluster) appendRaw(topic string, p int, batch []byte) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	fp := c.topics[topic][p]
	b := append([]byte(nil), batch...)
	binary.BigEndian.PutUint64(b, uint64(fp.next))
	base := fp.next
	fp.next += int64(int32(binary.BigEndian.Uint32(b[23:]))) + 1
	fp.batches = append(fp.batches, b)
	return base
}

func (b *fakeBroker) serve() {
	for {
		nc, err := b.ln.Accept()
		if err != nil {
			return
		}
		b.c.mu.Lock()
		down := b.down
		b.c.mu.Unlock()
		if down {
			nc.Close()
			continue
		}
		go b.conn(nc)
	}
}

func (b *fakeBroker) conn(nc net.Conn) {
	defer nc.Close()
	r := bufio.NewReader(nc)
	var scram *fakeScram
	for {
		var size [4]byte
		if _, err := io.ReadFull(r, size[:]); err != nil {
			return
		}
		req := make([]byte, binary.BigEndian.Uint32(size[:]))
		if _, err := io.ReadFull(r, req); err != nil {
			return
		}
		d := decoder{b: req}
		api, version, corr := d.int16(), d.int16(), d.int32()
		d.string() // client ID
		body := req[d.off:]
		b.c.mu.Lock()
		b.c.requests[api]++
		hook := b.c.hook
		b.c.mu.Unlock()
		action, resp := fakeNormal, []byte(nil)
		if hook != nil {
			action, resp = hook(b, api, version, body)
		}
		switch action {
		case fakeDrop:
			continue
		case fakeClose:
			return
		case fakeNormal:
			var ok bool
			if resp, ok = b.handle(api, version, body, &scram); !ok {
				continue // no answer: a produce with no acks
			}
		}
		var e encoder
		e.int32(int32(4 + len(resp)))
		if action == fakeBadCorr {
			corr++
		}
		e.int32(corr)
		e.b = append(e.b, resp...)
		switch action {
		case fakeCutHeader:
			nc.Write(e.b[:6])
			return
		case fakeCutBody:
			nc.Write(e.b[:8+len(resp)/2])
			return
		}
		if _, err := nc.Write(e.b); err != nil {
			return
		}
	}
}

func (b *fakeBroker) handle(api, v int16, body []byte, scram **fakeScram) ([]byte, bool) {
	c := b.c
	d := decoder{b: body}
	var e encoder
	switch api {
	case apiApiVersions:
		e.int16(0)
		c.mu.Lock()
		e.int32(int32(len(apiRange)))
		for k, r := range apiRange {
			if o, ok := c.versions[k]; ok {
				r = o
			}
			e.int16(k)
			e.int16(r[0])
			e.int16(r[1])
		}
		c.mu.Unlock()
	case apiMetadata:
		var names []string
		all := false
		if n := d.int32(); n < 0 {
			all = true
		} else {
			for range n {
				names = append(names, d.string())
			}
		}
		c.mu.Lock()
		if all {
			for n := range c.topics {
				names = append(names, n)
			}
		}
		if v >= 3 {
			e.int32(0)
		}
		e.int32(int32(len(c.brokers)))
		for _, br := range c.brokers {
			e.int32(br.id)
			e.string(br.host)
			e.int32(br.port)
			rack := "r" + strconv.Itoa(int(br.id))
			e.nullableString(&rack)
		}
		if v >= 2 {
			id := "fake"
			e.nullableString(&id)
		}
		e.int32(c.controller)
		e.int32(int32(len(names)))
		for _, n := range names {
			parts, ok := c.topics[n]
			if ok {
				e.int16(0)
			} else {
				e.int16(int16(ErrUnknownTopicOrPartition))
			}
			e.string(n)
			e.bool(false)
			e.int32(int32(len(parts)))
			for i := len(parts) - 1; i >= 0; i-- { // out of order: the client sorts
				p := parts[i]
				e.int16(int16(p.err))
				e.int32(int32(i))
				e.int32(p.leader)
				if v >= 7 {
					e.int32(1)
				}
				e.int32(int32(len(c.brokers)))
				for _, br := range c.brokers {
					e.int32(br.id)
				}
				e.int32(1)
				e.int32(p.leader)
				if v >= 5 {
					e.int32(0)
				}
			}
			if v >= 8 {
				e.int32(0)
			}
		}
		if v >= 8 {
			e.int32(0)
		}
		c.mu.Unlock()
	case apiProduce:
		d.nullableString()
		acks := d.int16()
		d.int32()
		type pr struct {
			p    int32
			code Error
			base int64
		}
		type tr struct {
			name  string
			parts []pr
		}
		var out []tr
		for range d.arrayLen() {
			t := tr{name: d.string()}
			for range d.arrayLen() {
				p := d.int32()
				set := d.bytes()
				code, base := b.produce(t.name, p, set)
				t.parts = append(t.parts, pr{p, code, base})
			}
			out = append(out, t)
		}
		if acks == 0 {
			return nil, false
		}
		e.int32(int32(len(out)))
		for _, t := range out {
			e.string(t.name)
			e.int32(int32(len(t.parts)))
			for _, p := range t.parts {
				e.int32(p.p)
				e.int16(int16(p.code))
				e.int64(p.base)
				e.int64(time.Now().UnixMilli())
				if v >= 5 {
					e.int64(0)
				}
				if v >= 8 {
					e.int32(0)
					e.nullableString(nil)
				}
			}
		}
		e.int32(0)
	case apiFetch:
		d.int32()
		maxWait := time.Duration(d.int32()) * time.Millisecond
		d.int32()
		d.int32()
		d.int8()
		if v >= 7 {
			d.int32()
			d.int32()
		}
		type fp struct {
			p      int32
			offset int64
		}
		var topics []string
		asked := map[string][]fp{}
		for range d.arrayLen() {
			t := d.string()
			topics = append(topics, t)
			for range d.arrayLen() {
				p := d.int32()
				if v >= 9 {
					d.int32()
				}
				off := d.int64()
				if v >= 5 {
					d.int64()
				}
				d.int32()
				asked[t] = append(asked[t], fp{p, off})
			}
		}
		// Wait for data, as a broker does, up to maxWait.
		deadline := time.Now().Add(maxWait)
		for time.Now().Before(deadline) {
			c.mu.Lock()
			has := false
			for t, ps := range asked {
				for _, p := range ps {
					// Data, or an error to answer at once.
					if parts := c.topics[t]; int(p.p) >= len(parts) || parts[p.p].leader != b.id || p.offset != parts[p.p].next {
						has = true
					}
				}
			}
			c.mu.Unlock()
			if has {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		e.int32(0)
		if v >= 7 {
			e.int16(0)
			e.int32(0)
		}
		c.mu.Lock()
		e.int32(int32(len(topics)))
		for _, t := range topics {
			e.string(t)
			e.int32(int32(len(asked[t])))
			for _, p := range asked[t] {
				e.int32(p.p)
				parts := c.topics[t]
				var part *fakePart
				code := Error(0)
				switch {
				case int(p.p) >= len(parts):
					code = ErrUnknownTopicOrPartition
				case parts[p.p].leader != b.id:
					code = ErrNotLeaderOrFollower
				default:
					part = parts[p.p]
					if p.offset > part.next || p.offset < 0 {
						code = ErrOffsetOutOfRange
					}
				}
				e.int16(int16(code))
				hw := int64(-1)
				if part != nil {
					hw = part.next
				}
				e.int64(hw)
				if v >= 4 {
					e.int64(hw)
				}
				if v >= 5 {
					e.int64(0)
				}
				if v >= 4 {
					if part == nil || part.aborted == nil {
						e.int32(-1)
					} else {
						e.int32(int32(len(part.aborted)))
						for _, a := range part.aborted {
							e.int64(a.producerID)
							e.int64(a.firstOffset)
						}
					}
				}
				if v >= 11 {
					e.int32(-1)
				}
				var set []byte
				if code == 0 {
					for _, bt := range part.batches {
						last := int64(binary.BigEndian.Uint64(bt)) + int64(int32(binary.BigEndian.Uint32(bt[23:])))
						if last >= p.offset {
							set = append(set, bt...)
						}
					}
				}
				e.bytes(set)
			}
		}
		c.mu.Unlock()
	case apiListOffsets:
		d.int32()
		if v >= 2 {
			d.int8()
			e.int32(0)
		}
		c.mu.Lock()
		n := d.arrayLen()
		e.int32(int32(n))
		for range n {
			t := d.string()
			e.string(t)
			m := d.arrayLen()
			e.int32(int32(m))
			for range m {
				p := d.int32()
				if v >= 4 {
					d.int32()
				}
				ts := d.int64()
				e.int32(p)
				parts := c.topics[t]
				switch {
				case int(p) >= len(parts):
					e.int16(int16(ErrUnknownTopicOrPartition))
					e.int64(-1)
					e.int64(-1)
				case parts[p].leader != b.id:
					e.int16(int16(ErrNotLeaderOrFollower))
					e.int64(-1)
					e.int64(-1)
				default:
					e.int16(0)
					e.int64(ts)
					switch ts {
					case OffsetEarliest:
						e.int64(0)
					case OffsetLatest:
						e.int64(parts[p].next)
					default:
						e.int64(parts[p].next / 2)
					}
				}
				if v >= 4 {
					e.int32(1)
				}
			}
		}
		c.mu.Unlock()
	case apiCreateTopics, apiDeleteTopics:
		c.mu.Lock()
		notController := c.controller != b.id && int(c.controller) <= len(c.brokers)
		e.int32(0)
		n := d.arrayLen()
		e.int32(int32(n))
		for range n {
			name := d.string()
			code := Error(0)
			msg := ""
			if api == apiCreateTopics {
				np := d.int32()
				d.int16()
				d.arrayLen()
				for range d.arrayLen() {
					d.string()
					d.nullableString()
				}
				if _, ok := c.topics[name]; ok {
					code, msg = ErrTopicAlreadyExists, "Topic '"+name+"' already exists."
				} else if !notController {
					for i := range max(np, 1) {
						c.topics[name] = append(c.topics[name], &fakePart{leader: i%int32(len(c.brokers)) + 1})
					}
				}
			} else if _, ok := c.topics[name]; !ok {
				code = ErrUnknownTopicOrPartition
			} else if !notController {
				delete(c.topics, name)
			}
			if notController {
				code = ErrNotController
			}
			e.string(name)
			e.int16(int16(code))
			if api == apiCreateTopics {
				if msg == "" {
					e.nullableString(nil)
				} else {
					e.nullableString(&msg)
				}
			}
		}
		c.mu.Unlock()
	case apiSaslHandshake:
		mech := d.string()
		ok := c.users != nil && (mech == SASLPlain || mech == SASLScramSHA256 || mech == SASLScramSHA512)
		if ok {
			e.int16(0)
		} else {
			e.int16(int16(ErrUnsupportedSASLMechanism))
		}
		e.int32(3)
		e.string(SASLPlain)
		e.string(SASLScramSHA256)
		e.string(SASLScramSHA512)
		if ok {
			*scram = &fakeScram{mech: mech}
		}
	case apiSaslAuthenticate:
		msg := d.bytes()
		out, err := (*scram).step(c, msg)
		if err != "" {
			e.int16(int16(ErrSASLAuthenticationFailed))
			e.nullableString(&err)
			e.bytes(nil)
		} else {
			e.int16(0)
			e.nullableString(nil)
			e.bytes(out)
		}
		e.int64(c.lifetime)
	default:
		c.t.Errorf("fake broker: API %d", api)
	}
	return e.b, true
}

// produce checks a record set and keeps its batches.
func (b *fakeBroker) produce(topic string, p int32, set []byte) (Error, int64) {
	c := b.c
	c.mu.Lock()
	parts, ok := c.topics[topic]
	if !ok || int(p) >= len(parts) {
		c.mu.Unlock()
		return ErrUnknownTopicOrPartition, -1
	}
	if parts[p].leader != b.id {
		c.mu.Unlock()
		return ErrNotLeaderOrFollower, -1
	}
	c.mu.Unlock()
	br := batchReader{maxBatchBytes: 1 << 26, dec: &decompressor{max: 1 << 26}}
	if _, _, err := br.read(set, 0, nil); err != nil {
		return ErrCorruptMessage, -1
	}
	return ErrNone, c.appendRaw(topic, int(p), set)
}

// ErrNone is no error, for the fake's answers.
const ErrNone Error = 0

// fakeScram is the server's side of PLAIN and SCRAM.
type fakeScram struct {
	mech               string
	user, first, nonce string
	salt               []byte
	serverFirst        string
}

func (s *fakeScram) step(c *fakeCluster, msg []byte) ([]byte, string) {
	c.mu.Lock()
	users := c.users
	c.mu.Unlock()
	if s.mech == SASLPlain {
		f := strings.Split(string(msg), "\x00")
		if len(f) != 3 || users[f[1]] != f[2] || f[2] == "" {
			return nil, "Authentication failed: Invalid username or password"
		}
		return nil, ""
	}
	h := sha256.New
	if s.mech == SASLScramSHA512 {
		h = sha512.New
	}
	if s.first == "" {
		s.first = strings.TrimPrefix(string(msg), "n,,")
		a := scramAttrs(s.first)
		s.user = strings.NewReplacer("=2C", ",", "=3D", "=").Replace(a["n"])
		s.nonce = a["r"] + "fakeserver"
		s.salt = []byte("fake salt")
		s.serverFirst = "r=" + s.nonce + ",s=" + base64.StdEncoding.EncodeToString(s.salt) + ",i=4096"
		return []byte(s.serverFirst), ""
	}
	a := scramAttrs(string(msg))
	final := "c=biws,r=" + s.nonce
	pw, ok := users[s.user]
	salted, _ := pbkdf2.Key(h, pw, s.salt, 4096, h().Size())
	mac := func(k []byte, m string) []byte { x := hmac.New(h, k); x.Write([]byte(m)); return x.Sum(nil) }
	authMessage := s.first + "," + s.serverFirst + "," + final
	clientKey := mac(salted, "Client Key")
	proof, _ := base64.StdEncoding.DecodeString(a["p"])
	want := mac(storedKeyOf(h, clientKey), authMessage)
	for i := range want {
		want[i] ^= clientKey[i]
	}
	if !ok || !hmac.Equal(proof, want) {
		return nil, "Authentication failed during authentication due to invalid credentials with SASL mechanism " + s.mech
	}
	return []byte("v=" + base64.StdEncoding.EncodeToString(mac(mac(salted, "Server Key"), authMessage))), ""
}

func storedKeyOf(h func() hash.Hash, k []byte) []byte {
	x := h()
	x.Write(k)
	return x.Sum(nil)
}

// fakeClient is a client of the cluster with short retries.
func fakeClient(t *testing.T, c *fakeCluster, mod func(*Config)) *Client {
	t.Helper()
	cfg := Config{Brokers: c.addrs()[:1], RetryFor: 3 * time.Second, RetryBackoff: 5 * time.Millisecond, RequestTimeout: 5 * time.Second}
	if mod != nil {
		mod(&cfg)
	}
	cl, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cl.Close() })
	return cl
}

func testCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute) // a hang guard
	t.Cleanup(cancel)
	return ctx
}
