package l4

import (
	"net/netip"
	"strings"
	"time"

	fib "github.com/lesismal/fib"
)

// OpeningTimeout bounds how long a connection may take to send what a
// listener reads before a service takes it: its PROXY header and its TLS
// ClientHello.
const OpeningTimeout = 10 * time.Second

// Entry is what a tcp or udp listener hands its connections to: its
// services, picked by the server name a TLS client asks for (SNI) when any
// has server_names, and the addresses it takes PROXY headers from.
type Entry struct {
	fallback *Service
	exact    map[string]*Service
	// wild holds the services of names "*.d", by d.
	wild    map[string]*Service
	trusted Trusted
}

// NewEntry returns an entry that takes PROXY headers from trusted, or from
// no one when it is empty.
func NewEntry(trusted Trusted) *Entry { return &Entry{trusted: trusted} }

// Add has s take the connections that ask for one of names, or those no
// other service's names take when names is empty.
func (e *Entry) Add(s *Service, names []string) {
	if len(names) == 0 {
		e.fallback = s
		return
	}
	for _, name := range names {
		name = strings.ToLower(name)
		if d, ok := strings.CutPrefix(name, "*."); ok {
			if e.wild == nil {
				e.wild = map[string]*Service{}
			}
			e.wild[d] = s
			continue
		}
		if e.exact == nil {
			e.exact = map[string]*Service{}
		}
		e.exact[name] = s
	}
}

// routes reports whether the entry picks services by SNI.
func (e *Entry) routes() bool { return e.exact != nil || e.wild != nil }

// pick returns the service for the server name name, which "" is for a
// client that asked for none or did not speak TLS.
func (e *Entry) pick(name string) *Service {
	if s := e.exact[name]; s != nil {
		return s
	}
	if _, d, ok := strings.Cut(name, "."); ok {
		if s := e.wild[d]; s != nil {
			return s
		}
	}
	return e.fallback
}

// Open hands c, a connection one of the entry's listeners has just
// accepted, or a udp listener's new session, to its service, from the
// listener's OnOpen; Clients serves its callbacks from then on. First, when
// it comes from a trusted address, its PROXY header is read, and when the
// entry routes by SNI, its ClientHello. release is called once, when the
// connection is over.
func (e *Entry) Open(c *fib.Connection, release func()) {
	proxy := !c.IsUDP() && len(e.trusted) > 0 && e.trusted.Contains(c.RemoteAddrPort().Addr())
	if !proxy && (!e.routes() || c.IsUDP()) {
		if e.fallback == nil {
			_ = c.Close()
			release()
			return
		}
		e.fallback.start(c, release, nil, netip.AddrPort{})
		return
	}
	c.SetAttachment(&opening{e: e, release: release, proxy: proxy})
	_ = c.SetReadDeadline(time.Now().Add(OpeningTimeout))
}

// opening is a connection whose PROXY header or ClientHello is still being
// read.
type opening struct {
	e       *Entry
	release func()
	proxy   bool
	buf     []byte
	// dst is where the PROXY header says the client connected to.
	dst netip.AddrPort
}

// data takes what the client sent, until the service is known.
func (o *opening) data(c *fib.Connection, data []byte) {
	o.buf = append(o.buf, data...)
	if o.proxy {
		n, src, dst, err := parseProxy(o.buf)
		switch {
		case err != nil || n == 0 && len(o.buf) >= MaxProxyHeader:
			_ = c.Close()
			return
		case n == 0:
			return
		}
		if src.IsValid() {
			c.SetRemoteAddrPort(src)
		}
		o.dst, o.proxy = dst, false
		o.buf = o.buf[n:]
	}
	name := ""
	if o.e.routes() {
		if len(o.buf) == 0 {
			return
		}
		var done bool
		var err error
		name, done, err = readSNI(o.buf)
		switch {
		case err != nil:
			// Not TLS, or not a hello fit can read: the fallback's.
			name = ""
		case !done && len(o.buf) < MaxClientHello:
			return
		}
	}
	s := o.e.pick(name)
	if s == nil {
		_ = c.Close()
		return
	}
	_ = c.SetReadDeadline(time.Time{})
	s.start(c, o.release, o.buf, o.dst)
}

// Conns returns how many connections and udp sessions the entry's services
// proxy now, theirs from other listeners too.
func (e *Entry) Conns() int64 {
	seen := map[*Service]bool{}
	var n int64
	add := func(s *Service) {
		if s != nil && !seen[s] {
			seen[s] = true
			n += s.Conns()
		}
	}
	add(e.fallback)
	for _, s := range e.exact {
		add(s)
	}
	for _, s := range e.wild {
		add(s)
	}
	return n
}
