package sharedstore

import (
	"context"
	"crypto/sha1"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lesismal/fib/bittorrent/metainfo"
	fibtracker "github.com/lesismal/fib/bittorrent/tracker"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
	"github.com/lesismal/fit/sync/redis/wire"
)

// The keys, under the prefix P:
//
//	P torrents        a sorted set of the torrents' info hashes in hex, each
//	                  scored by the latest expiry of its peers (ms, Redis's
//	                  clock): the index max_torrents bounds and sweeps read
//	P peers           how many peers are kept in all, for max_peers
//	P t/<hash>/s      a sorted set of the seeders' peer IDs, by expiry
//	P t/<hash>/l      the leechers alike
//	P t/<hash>/a      a hash of each peer's addresses, by peer ID
//	P t/<hash>/n      a hash of the torrent's completed count, field d
//
// Every change is made by a script, so that the counts stay true: a peer is
// in one of its torrent's sets at most, and a torrent left without peers
// loses its keys and leaves the index. Keys never expire of themselves;
// expired peers are removed by the announces of their torrent and, a whole
// torrent at a time, by sweeps.

// redisNow is Redis's clock in milliseconds, in a script.
const redisNow = `local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
`

// scriptAnnounce writes an announce. KEYS: index, seeders, leechers,
// addresses, counts, peers. ARGV: the hash in hex, the peer ID, its
// addresses, 1 for a seeder, the event (0, 1 completed, 3 stopped), the
// write's age in ms, the TTL in ms, max_torrents, max_peers_per_torrent,
// max_peers. It returns a result.
const scriptAnnounce = redisNow + `local function prune(k)
  local ex = redis.call('ZRANGEBYSCORE', k, '-inf', now, 'LIMIT', 0, 256)
  if #ex > 0 then
    redis.call('ZREM', k, unpack(ex))
    redis.call('HDEL', KEYS[4], unpack(ex))
    redis.call('DECRBY', KEYS[6], #ex)
  end
end
local function cleanup()
  if redis.call('ZCARD', KEYS[2]) + redis.call('ZCARD', KEYS[3]) == 0 then
    redis.call('DEL', KEYS[2], KEYS[3], KEYS[4], KEYS[5])
    redis.call('ZREM', KEYS[1], ARGV[1])
  end
end
prune(KEYS[2])
prune(KEYS[3])
local id = ARGV[2]
local seeder = ARGV[4] == '1'
local mine, other = KEYS[3], KEYS[2]
if seeder then mine, other = KEYS[2], KEYS[3] end
local inMine = redis.call('ZSCORE', mine, id)
local inOther = redis.call('ZSCORE', other, id)
if ARGV[5] == '3' then
  if inMine or inOther then
    redis.call('ZREM', mine, id)
    redis.call('ZREM', other, id)
    redis.call('HDEL', KEYS[4], id)
    redis.call('DECR', KEYS[6])
  end
  cleanup()
  return 0
end
local expires = now - tonumber(ARGV[6]) + tonumber(ARGV[7])
if expires <= now then
  cleanup()
  return 0
end
if not inMine and not inOther then
  if not redis.call('ZSCORE', KEYS[1], ARGV[1]) and redis.call('ZCARD', KEYS[1]) >= tonumber(ARGV[8]) then return 1 end
  if redis.call('ZCARD', KEYS[2]) + redis.call('ZCARD', KEYS[3]) >= tonumber(ARGV[9]) then
    cleanup()
    return 2
  end
  if tonumber(redis.call('GET', KEYS[6]) or '0') >= tonumber(ARGV[10]) then
    cleanup()
    return 3
  end
  redis.call('INCR', KEYS[6])
end
if inOther then redis.call('ZREM', other, id) end
redis.call('ZADD', mine, expires, id)
redis.call('HSET', KEYS[4], id, ARGV[3])
if ARGV[5] == '1' and seeder and not inMine then redis.call('HINCRBY', KEYS[5], 'd', 1) end
local cur = redis.call('ZSCORE', KEYS[1], ARGV[1])
if not cur or tonumber(cur) < expires then redis.call('ZADD', KEYS[1], expires, ARGV[1]) end
return 0
`

// scriptFetch reads a sample of a torrent. KEYS: seeders, leechers,
// addresses, counts. ARGV: the peers of each kind wanted. It returns
// Redis's time, the seeders, the leechers and the completed count, then
// for the seeders and the leechers each a list of ID, expiry, addresses.
const scriptFetch = redisNow + `local out = {now, redis.call('ZCOUNT', KEYS[1], '(' .. now, '+inf'),
  redis.call('ZCOUNT', KEYS[2], '(' .. now, '+inf'), tonumber(redis.call('HGET', KEYS[4], 'd') or '0')}
local n = tonumber(ARGV[1])
for i = 1, 2 do
  local list = {}
  if n > 0 then
    local r = redis.call('ZRANDMEMBER', KEYS[i], n, 'WITHSCORES')
    local ids, scores = {}, {}
    for j = 1, #r, 2 do
      if tonumber(r[j + 1]) > now then
        ids[#ids + 1] = r[j]
        scores[#scores + 1] = r[j + 1]
      end
    end
    if #ids > 0 then
      local addrs = redis.call('HMGET', KEYS[3], unpack(ids))
      for j = 1, #ids do
        if addrs[j] then
          list[#list + 1] = ids[j]
          list[#list + 1] = scores[j]
          list[#list + 1] = addrs[j]
        end
      end
    end
  end
  out[#out + 1] = list
end
return out
`

// scriptExpired lists torrents whose peers have all expired. KEYS: index.
// ARGV: how many at most.
const scriptExpired = redisNow + `return redis.call('ZRANGEBYSCORE', KEYS[1], '-inf', now, 'LIMIT', 0, tonumber(ARGV[1]))`

// scriptDrop removes a torrent whose peers have all expired, if they still
// have. KEYS: index, seeders, leechers, addresses, counts, peers. ARGV: the
// hash in hex.
const scriptDrop = redisNow + `local sc = redis.call('ZSCORE', KEYS[1], ARGV[1])
if sc and tonumber(sc) > now then return 0 end
local n = redis.call('ZCARD', KEYS[2]) + redis.call('ZCARD', KEYS[3])
redis.call('DEL', KEYS[2], KEYS[3], KEYS[4], KEYS[5])
if n > 0 then redis.call('DECRBY', KEYS[6], n) end
redis.call('ZREM', KEYS[1], ARGV[1])
return 1
`

type script struct{ src, sha string }

func newScript(src string) *script {
	sum := sha1.Sum([]byte(src))
	return &script{src: src, sha: hex.EncodeToString(sum[:])}
}

var (
	luaAnnounce = newScript(scriptAnnounce)
	luaFetch    = newScript(scriptFetch)
	luaExpired  = newScript(scriptExpired)
	luaDrop     = newScript(scriptDrop)
)

// call is a script run of a pipeline.
type call struct {
	s    *script
	keys []string
	args []string
}

func (c call) cmd(eval bool) []string {
	op, body := "EVALSHA", c.s.sha
	if eval {
		op, body = "EVAL", c.s.src
	}
	out := make([]string, 0, 3+len(c.keys)+len(c.args))
	out = append(out, op, body, strconv.Itoa(len(c.keys)))
	out = append(out, c.keys...)
	return append(out, c.args...)
}

// redisBackend keeps the swarms in Redis: the store's goroutine on one
// connection, scans on another.
type redisBackend struct {
	opts    wire.Options
	pwFile  string
	prefix  string
	timeout time.Duration

	loop *wire.Conn // the store's goroutine's

	ctlMu sync.Mutex
	ctl   *wire.Conn
}

// Redis limits what a reply may hold: a sample of 1000 peers of each kind
// is some 3000 strings.
var redisLimits = wire.Limits{MaxBulk: 1 << 20, MaxElems: 1 << 20, MaxBytes: 64 << 20}

// NewRedis returns a store shared through the Redis c, its keys starting
// with prefix. It does not connect: its goroutine does, and goes on doing
// so while the server cannot be reached. An error is a TLS file that does
// not read.
func NewRedis(c *config.RedisStorage, prefix string, cfg Config, log *logs.Logger) (*Store, error) {
	cfg = cfg.withDefaults()
	b := &redisBackend{
		opts: wire.Options{Addr: c.Addr(), Username: c.Username, DB: c.DB, Protocol: 2,
			DialTimeout: cfg.Timeout, ClientName: "fit-bittorrent-tracker", Limits: redisLimits},
		pwFile: c.PasswordFile, prefix: prefix, timeout: cfg.Timeout,
	}
	if c.TLS {
		host, _, _ := net.SplitHostPort(c.Addr())
		tc := &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12}
		if c.CAFile != "" {
			pem, err := os.ReadFile(c.CAFile)
			if err != nil {
				return nil, err
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return nil, fmt.Errorf("%s: no PEM certificates", c.CAFile)
			}
			tc.RootCAs = pool
		}
		if c.CertFile != "" {
			cert, err := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
			if err != nil {
				return nil, err
			}
			tc.Certificates = []tls.Certificate{cert}
		}
		b.opts.TLS = tc
	}
	return newStore(b, cfg, log), nil
}

func (r *redisBackend) kind() string { return "redis" }

// conn returns *cp, dialled anew when it is nil or broken.
func (r *redisBackend) conn(ctx context.Context, cp **wire.Conn) (*wire.Conn, error) {
	if c := *cp; c != nil && c.Broken() == nil {
		return c, nil
	}
	o := r.opts
	if r.pwFile != "" {
		data, err := os.ReadFile(r.pwFile)
		if err != nil {
			return nil, err
		}
		o.Password = strings.TrimRight(string(data), "\r\n")
	}
	c, err := wire.Dial(ctx, o)
	if err != nil {
		return nil, err
	}
	*cp = c
	return c, nil
}

// run runs calls in one pipeline on *cp, those Redis does not have the
// script of again with the script's source, and returns their replies.
func (r *redisBackend) run(ctx context.Context, cp **wire.Conn, calls []call) ([]wire.Value, error) {
	c, err := r.conn(ctx, cp)
	if err != nil {
		return nil, err
	}
	cmds := make([][]string, len(calls))
	for i, cl := range calls {
		cmds[i] = cl.cmd(false)
	}
	vs, err := c.Pipeline(ctx, cmds)
	if err != nil {
		return nil, err
	}
	var again []int
	for i, v := range vs {
		var we *wire.Error
		if err := v.Err(); err != nil && errors.As(err, &we) && we.Code() == "NOSCRIPT" {
			again = append(again, i)
		}
	}
	if len(again) > 0 {
		cmds = cmds[:0]
		for _, i := range again {
			cmds = append(cmds, calls[i].cmd(true))
		}
		more, err := c.Pipeline(ctx, cmds)
		if err != nil {
			return nil, err
		}
		for j, i := range again {
			vs[i] = more[j]
		}
	}
	return vs, nil
}

func (r *redisBackend) torrentKeys(hexHash string) (s, l, a, n string) {
	base := r.prefix + "t/" + hexHash
	return base + "/s", base + "/l", base + "/a", base + "/n"
}

// encodeAddrs packs a peer's addresses: a byte of flags (1 IPv4, 2 IPv6),
// then 6 bytes of IPv4 and port, then 18 of IPv6 and port, each if set.
func encodeAddrs(v4, v6 netip.AddrPort) string {
	b := make([]byte, 1, 25)
	if v4.IsValid() && v4.Addr().Unmap().Is4() {
		b[0] |= 1
		a := v4.Addr().Unmap().As4()
		b = append(b, a[:]...)
		b = append(b, byte(v4.Port()>>8), byte(v4.Port()))
	}
	if v6.IsValid() {
		b[0] |= 2
		a := v6.Addr().As16()
		b = append(b, a[:]...)
		b = append(b, byte(v6.Port()>>8), byte(v6.Port()))
	}
	return string(b)
}

func decodeAddrs(s string) (v4, v6 netip.AddrPort, ok bool) {
	if len(s) < 1 {
		return v4, v6, false
	}
	flags, rest := s[0], s[1:]
	if flags&1 != 0 {
		if len(rest) < 6 {
			return v4, v6, false
		}
		v4 = netip.AddrPortFrom(netip.AddrFrom4([4]byte([]byte(rest[:4]))), uint16(rest[4])<<8|uint16(rest[5]))
		rest = rest[6:]
	}
	if flags&2 != 0 {
		if len(rest) < 18 {
			return v4, v6, false
		}
		v6 = netip.AddrPortFrom(netip.AddrFrom16([16]byte([]byte(rest[:16]))), uint16(rest[16])<<8|uint16(rest[17]))
		rest = rest[18:]
	}
	return v4, v6, len(rest) == 0 && flags&^3 == 0
}

func (r *redisBackend) write(ctx context.Context, ws []*write, b bounds, now time.Time) ([]result, error) {
	idx, peers := r.prefix+"torrents", r.prefix+"peers"
	ttl := strconv.FormatInt(b.ttl.Milliseconds(), 10)
	mt, mp, ma := strconv.Itoa(b.torrents), strconv.Itoa(b.perTorrent), strconv.Itoa(b.peers)
	calls := make([]call, len(ws))
	for i, w := range ws {
		hh := hex.EncodeToString(w.h[:])
		s, l, a, n := r.torrentKeys(hh)
		seeder, event := "0", "0"
		if w.seeder {
			seeder = "1"
		}
		switch {
		case w.stopped:
			event = "3"
		case w.completed:
			event = "1"
		}
		age := max(now.Sub(w.at).Milliseconds(), 0)
		calls[i] = call{s: luaAnnounce, keys: []string{idx, s, l, a, n, peers},
			args: []string{hh, string(w.id[:]), encodeAddrs(w.v4, w.v6), seeder, event, strconv.FormatInt(age, 10), ttl, mt, mp, ma}}
	}
	vs, err := r.run(ctx, &r.loop, calls)
	if err != nil {
		return nil, err
	}
	out := make([]result, len(vs))
	for i, v := range vs {
		if v.Kind != wire.KindInt || v.Int < 0 || v.Int > int64(resultPeersFull) {
			out[i] = resultFailed
			continue
		}
		out[i] = result(v.Int)
	}
	return out, nil
}

func (r *redisBackend) fetch(ctx context.Context, hs []metainfo.InfoHash, peers int, now time.Time) ([]*snapshot, error) {
	calls := make([]call, len(hs))
	for i, h := range hs {
		s, l, a, n := r.torrentKeys(hex.EncodeToString(h[:]))
		calls[i] = call{s: luaFetch, keys: []string{s, l, a, n}, args: []string{strconv.Itoa(peers)}}
	}
	vs, err := r.run(ctx, &r.loop, calls)
	if err != nil {
		return nil, err
	}
	out := make([]*snapshot, len(vs))
	for i, v := range vs {
		out[i] = parseSample(v, now)
	}
	return out, nil
}

// parseSample reads scriptFetch's reply, nil for one it cannot read; the
// peers' expiries are moved to this instance's clock, at now.
func parseSample(v wire.Value, now time.Time) *snapshot {
	if v.Kind != wire.KindArray || len(v.Elems) != 6 {
		return nil
	}
	e := v.Elems
	for _, x := range e[:4] {
		if x.Kind != wire.KindInt {
			return nil
		}
	}
	redisMs := e[0].Int
	sn := &snapshot{at: now, stats: fibtracker.Stats{Complete: int(e[1].Int), Incomplete: int(e[2].Int), Downloaded: int(e[3].Int)}}
	for i, list := range e[4:] {
		which := 1 - i // the seeders first: lists are leechers, seeders
		for j := 0; j+2 < len(list.Elems); j += 3 {
			id, score, addrs := list.Elems[j].Str, list.Elems[j+1].Str, list.Elems[j+2].Str
			if len(id) != len(fibtracker.PeerID{}) {
				continue
			}
			exp, err := strconv.ParseFloat(score, 64)
			if err != nil {
				continue
			}
			v4, v6, ok := decodeAddrs(addrs)
			if !ok {
				continue
			}
			p := remotePeer{v4: v4, v6: v6, expires: now.Add(time.Duration(int64(exp)-redisMs) * time.Millisecond)}
			copy(p.id[:], id)
			sn.lists[which] = append(sn.lists[which], p)
		}
	}
	return sn
}

func (r *redisBackend) totals(ctx context.Context) (int, int, error) {
	c, err := r.conn(ctx, &r.loop)
	if err != nil {
		return 0, 0, err
	}
	vs, err := c.Pipeline(ctx, [][]string{{"ZCARD", r.prefix + "torrents"}, {"GET", r.prefix + "peers"}})
	if err != nil {
		return 0, 0, err
	}
	for _, v := range vs {
		if err := v.Err(); err != nil {
			return 0, 0, err
		}
	}
	p := 0
	if s, ok := vs[1].Text(); ok {
		p, _ = strconv.Atoi(s)
	}
	return int(vs[0].Int), max(p, 0), nil
}

func (r *redisBackend) sweep(ctx context.Context, max int) (int, error) {
	idx := r.prefix + "torrents"
	vs, err := r.run(ctx, &r.loop, []call{{s: luaExpired, keys: []string{idx}, args: []string{strconv.Itoa(max)}}})
	if err != nil {
		return 0, err
	}
	if err := vs[0].Err(); err != nil {
		return 0, err
	}
	if len(vs[0].Elems) == 0 {
		return 0, nil
	}
	calls := make([]call, 0, len(vs[0].Elems))
	for _, x := range vs[0].Elems {
		s, l, a, n := r.torrentKeys(x.Str)
		calls = append(calls, call{s: luaDrop, keys: []string{idx, s, l, a, n, r.prefix + "peers"}, args: []string{x.Str}})
	}
	dropped, err := r.run(ctx, &r.loop, calls)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, v := range dropped {
		if err := v.Err(); err != nil {
			return n, err
		}
		n += int(v.Int)
	}
	// What was listed counts, dropped or not, so that a batch of torrents
	// announced to meanwhile does not end the sweep early.
	return len(calls), nil
}

func (r *redisBackend) scan(ctx context.Context, fn func(metainfo.InfoHash, fibtracker.Stats) bool) error {
	r.ctlMu.Lock()
	defer r.ctlMu.Unlock()
	idx := r.prefix + "torrents"
	cursor := "0"
	for seen := 0; seen <= 2*config.MaxBTTrackerTorrents; {
		rctx, cancel := context.WithTimeout(ctx, r.timeout)
		c, err := r.conn(rctx, &r.ctl)
		if err != nil {
			cancel()
			return err
		}
		v, err := c.Do(rctx, "ZSCAN", idx, cursor, "COUNT", "512")
		cancel()
		if err != nil {
			return err
		}
		if err := v.Err(); err != nil {
			return err
		}
		if v.Kind != wire.KindArray || len(v.Elems) != 2 {
			return errors.New("ZSCAN: a reply that is not a cursor and members")
		}
		cursor = v.Elems[0].Str
		members := v.Elems[1].Elems
		var hs []metainfo.InfoHash
		for j := 0; j+1 < len(members); j += 2 {
			b, err := hex.DecodeString(members[j].Str)
			if err != nil || len(b) != len(metainfo.InfoHash{}) {
				continue
			}
			hs = append(hs, metainfo.InfoHash(b))
		}
		seen += len(hs)
		if len(hs) > 0 {
			rctx, cancel := context.WithTimeout(ctx, r.timeout)
			calls := make([]call, len(hs))
			for i, h := range hs {
				s, l, a, n := r.torrentKeys(hex.EncodeToString(h[:]))
				calls[i] = call{s: luaFetch, keys: []string{s, l, a, n}, args: []string{"0"}}
			}
			vs, err := r.run(rctx, &r.ctl, calls)
			cancel()
			if err != nil {
				return err
			}
			for i, v := range vs {
				sn := parseSample(v, time.Now())
				if sn == nil || sn.stats.Complete+sn.stats.Incomplete == 0 {
					continue
				}
				if !fn(hs[i], sn.stats) {
					return nil
				}
			}
		}
		if cursor == "0" || cursor == "" {
			return nil
		}
	}
	return nil
}

func (r *redisBackend) close() {
	if r.loop != nil {
		_ = r.loop.Close()
	}
	r.ctlMu.Lock()
	if r.ctl != nil {
		_ = r.ctl.Close()
	}
	r.ctlMu.Unlock()
}
