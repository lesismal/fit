package syncredis

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// server is an in-process stand-in for Redis, enough of it for the
// connectors: streams (XADD, XREAD, XREVRANGE, XLEN, XRANGE), strings (SET,
// GET), hashes (HSET, HGETALL), DEL, PEXPIRE, PTTL, MULTI and EXEC, in RESP2
// or RESP3 as the client says HELLO. The real servers' behaviour is
// test/sync's, against Redis and Valkey in Docker.
type server struct {
	t  *testing.T
	ln net.Listener

	mu      sync.Mutex
	streams map[string][]entryOf
	strs    map[string]string
	hashes  map[string][][2]string
	ttls    map[string]int64
	cmds    []string
	// noHello answers HELLO as a server older than Redis 6.
	noHello bool
	// inject, when set and returning true, answers a command with its raw
	// reply instead.
	inject func(args []string) (string, bool)
}

type entryOf struct {
	ms, seq uint64
	fields  []string
}

func (e entryOf) id() string { return fmt.Sprintf("%d-%d", e.ms, e.seq) }

func newServer(t *testing.T) *server {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := &server{t: t, ln: ln, streams: map[string][]entryOf{}, strs: map[string]string{}, hashes: map[string][][2]string{}, ttls: map[string]int64{}}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go s.serve(c)
		}
	}()
	return s
}

func (s *server) addr() string { return s.ln.Addr().String() }

func (s *server) sent() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.cmds)
}

func (s *server) reset() {
	s.mu.Lock()
	s.cmds = nil
	s.mu.Unlock()
}

func (s *server) setInject(f func(args []string) (string, bool)) {
	s.mu.Lock()
	s.inject = f
	s.mu.Unlock()
}

// The encoders of replies.
func bulk(v string) string { return "$" + strconv.Itoa(len(v)) + "\r\n" + v + "\r\n" }
func arr(elems ...string) string {
	return "*" + strconv.Itoa(len(elems)) + "\r\n" + strings.Join(elems, "")
}
func integer(n int64) string { return ":" + strconv.FormatInt(n, 10) + "\r\n" }

func (s *server) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	resp3, multi := false, false
	var queued [][]string
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		n, _ := strconv.Atoi(strings.TrimSpace(line[1:]))
		args := make([]string, n)
		for i := range args {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			l, _ := strconv.Atoi(strings.TrimSpace(line[1:]))
			buf := make([]byte, l+2)
			if _, err := io.ReadFull(r, buf); err != nil {
				return
			}
			args[i] = string(buf[:l])
		}
		var reply string
		switch strings.ToUpper(args[0]) {
		case "HELLO":
			s.mu.Lock()
			old := s.noHello
			s.mu.Unlock()
			if old {
				reply = "-ERR unknown command 'HELLO'\r\n"
			} else {
				resp3 = args[1] == "3"
				reply = "%1\r\n+proto\r\n:3\r\n"
			}
		case "CLIENT", "AUTH", "SELECT":
			reply = "+OK\r\n"
		case "MULTI":
			multi, queued = true, nil
			s.mu.Lock()
			s.cmds = append(s.cmds, "MULTI")
			s.mu.Unlock()
			reply = "+OK\r\n"
		case "EXEC":
			s.mu.Lock()
			s.cmds = append(s.cmds, "EXEC")
			s.mu.Unlock()
			var out []string
			for _, q := range queued {
				out = append(out, s.run(q, resp3))
			}
			multi = false
			reply = arr(out...)
		default:
			if multi {
				queued = append(queued, args)
				s.mu.Lock()
				s.cmds = append(s.cmds, strings.Join(args, " "))
				s.mu.Unlock()
				reply = "+QUEUED\r\n"
			} else {
				reply = s.do(args, resp3)
			}
		}
		if _, err := io.WriteString(c, reply); err != nil {
			return
		}
	}
}

const errWrongType = "-WRONGTYPE Operation against a key holding the wrong kind of value\r\n"

func (s *server) do(args []string, resp3 bool) string {
	s.mu.Lock()
	s.cmds = append(s.cmds, strings.Join(args, " "))
	s.mu.Unlock()
	return s.run(args, resp3)
}

func (s *server) run(args []string, resp3 bool) string {
	s.mu.Lock()
	inject := s.inject
	s.mu.Unlock()
	if inject != nil {
		if reply, ok := inject(args); ok {
			return reply
		}
	}
	null := "$-1\r\n"
	if resp3 {
		null = "_\r\n"
	}
	cmd := strings.ToUpper(args[0])
	if cmd == "XREAD" {
		return s.xread(args, resp3, null)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	switch cmd {
	case "XADD":
		key, i := args[1], 2
		if _, ok := s.strs[key]; ok {
			return errWrongType
		}
		if args[i] == "MAXLEN" {
			i += 3
		}
		st := s.streams[key]
		var e entryOf
		if args[i] == "*" {
			e.ms = uint64(time.Now().UnixMilli())
			if n := len(st); n > 0 && e.ms <= st[n-1].ms {
				e.ms, e.seq = st[n-1].ms, st[n-1].seq+1
			}
		} else {
			ms, seq, _ := ParseID(args[i])
			e.ms, e.seq = ms, seq
			if ms == 0 && seq == 0 {
				return "-ERR The ID specified in XADD must be greater than 0-0\r\n"
			}
			if n := len(st); n > 0 && (ms < st[n-1].ms || ms == st[n-1].ms && seq <= st[n-1].seq) {
				return "-ERR The ID specified in XADD is equal or smaller than the target stream top item\r\n"
			}
		}
		e.fields = slices.Clone(args[i+1:])
		s.streams[key] = append(st, e)
		return bulk(e.id())
	case "XLEN":
		return integer(int64(len(s.streams[args[1]])))
	case "XRANGE", "XREVRANGE":
		st := slices.Clone(s.streams[args[1]])
		if cmd == "XREVRANGE" {
			slices.Reverse(st)
		}
		if len(args) > 5 {
			n, _ := strconv.Atoi(args[5])
			st = st[:min(n, len(st))]
		}
		return entries(st)
	case "SET":
		if len(args) > 3 {
			ms, _ := strconv.ParseInt(args[4], 10, 64)
			s.ttls[args[1]] = ms
		} else {
			delete(s.ttls, args[1])
		}
		delete(s.hashes, args[1])
		s.strs[args[1]] = args[2]
		return "+OK\r\n"
	case "GET":
		v, ok := s.strs[args[1]]
		if !ok {
			return null
		}
		return bulk(v)
	case "DEL":
		_, a := s.strs[args[1]]
		_, b := s.hashes[args[1]]
		_, c := s.streams[args[1]]
		delete(s.strs, args[1])
		delete(s.hashes, args[1])
		delete(s.streams, args[1])
		delete(s.ttls, args[1])
		if a || b || c {
			return integer(1)
		}
		return integer(0)
	case "HSET":
		if _, ok := s.strs[args[1]]; ok {
			return errWrongType
		}
		h := s.hashes[args[1]]
		for i := 2; i+1 < len(args); i += 2 {
			h = append(h, [2]string{args[i], args[i+1]})
		}
		s.hashes[args[1]] = h
		return integer(int64(len(args)-2) / 2)
	case "HGETALL":
		var out []string
		for _, f := range s.hashes[args[1]] {
			out = append(out, bulk(f[0]), bulk(f[1]))
		}
		return arr(out...)
	case "PEXPIRE":
		ms, _ := strconv.ParseInt(args[2], 10, 64)
		s.ttls[args[1]] = ms
		return integer(1)
	case "PTTL":
		ms, ok := s.ttls[args[1]]
		if !ok {
			return integer(-1)
		}
		return integer(ms)
	}
	return "-ERR unknown command '" + args[0] + "'\r\n"
}

func entries(st []entryOf) string {
	out := make([]string, len(st))
	for i, e := range st {
		fs := make([]string, len(e.fields))
		for j, f := range e.fields {
			fs[j] = bulk(f)
		}
		out[i] = arr(bulk(e.id()), arr(fs...))
	}
	return arr(out...)
}

// xread answers XREAD [COUNT n] [BLOCK ms] STREAMS keys... ids..., a
// BLOCK waiting at most 50ms here.
func (s *server) xread(args []string, resp3 bool, null string) string {
	count, block := 1<<30, -1
	i := 1
	for ; i < len(args) && args[i] != "STREAMS"; i += 2 {
		n, _ := strconv.Atoi(args[i+1])
		if args[i] == "COUNT" {
			count = n
		} else {
			block = n
		}
	}
	rest := args[i+1:]
	keys, ids := rest[:len(rest)/2], rest[len(rest)/2:]
	deadline := time.Now().Add(time.Duration(min(block, 50)) * time.Millisecond)
	for {
		s.mu.Lock()
		var parts []string
		for k, key := range keys {
			ms, seq, _ := ParseID(ids[k])
			var got []entryOf
			for _, e := range s.streams[key] {
				if (e.ms > ms || e.ms == ms && e.seq > seq) && len(got) < count {
					got = append(got, e)
				}
			}
			if len(got) > 0 {
				if resp3 {
					parts = append(parts, bulk(key)+entries(got))
				} else {
					parts = append(parts, arr(bulk(key), entries(got)))
				}
			}
		}
		s.mu.Unlock()
		if len(parts) > 0 {
			if resp3 {
				return "%" + strconv.Itoa(len(parts)) + "\r\n" + strings.Join(parts, "")
			}
			return arr(parts...)
		}
		if block < 0 || time.Now().After(deadline) {
			if resp3 {
				return null
			}
			return "*-1\r\n"
		}
		time.Sleep(2 * time.Millisecond)
	}
}
