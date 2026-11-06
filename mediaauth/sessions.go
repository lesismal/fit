package mediaauth

import (
	"hash/maphash"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The playback sessions of a policy's signed URLs: a session is a grant's
// sid, of a subject, playing while its requests come within session_idle.
// url_signing's max_sessions caps those of a subject; on_limit refuses a new
// one past it, or ends the subject's oldest, whose requests are refused from
// then on, until its grants would have expired. The table is a policy's,
// kept across reloads, in shards by subject, each under a lock of its own
// held for a few comparisons; it never holds more than max_tracked_sessions.

const sessionShards = 64

// sweepEvery is how often, at most, a table at its bound is swept whole for
// sessions that are over.
const sweepEvery = time.Second

type sessionTable struct {
	shards [sessionShards]sessionShard
	total  atomic.Int64
	// swept is when the table was last swept whole, in Unix nanoseconds.
	swept atomic.Int64
}

type sessionShard struct {
	mu sync.Mutex
	// subjects are the sessions by subject. A subject's entry is changed in
	// place, never stored again, so that its key stays the copy first made.
	subjects map[string]*sessionList
}

type sessionList struct{ list []*session }

type session struct {
	sid      string
	resource string
	address  string
	start    int64 // Unix nanoseconds
	last     int64 // Unix nanoseconds
	keep     int64 // Unix seconds: the last of its grants' expiries
	ended    bool
}

var sessionSeed = maphash.MakeSeed()

func (t *sessionTable) shard(subject string) *sessionShard {
	return &t.shards[maphash.String(sessionSeed, subject)%sessionShards]
}

// over reports whether s is over at now: a playing session idle past idle,
// or an ended one past its grants' expiry.
func (s *session) over(now int64, idle time.Duration) bool {
	if s.ended {
		return s.keep <= now/int64(time.Second)
	}
	return s.last+int64(idle) < now
}

// prune drops the sessions of l that are over. The shard's lock is held.
func (t *sessionTable) prune(l *sessionList, now int64, idle time.Duration) {
	n := len(l.list)
	l.list = slices.DeleteFunc(l.list, func(s *session) bool { return s.over(now, idle) })
	if d := n - len(l.list); d > 0 {
		t.total.Add(-int64(d))
	}
}

// touch records a request of session sid of subject, for resource, from
// address, with a grant good until exp, at now: nil for a session playing
// or let begin, or why it is refused.
func (t *sessionTable) touch(subject, sid, resource, address string, exp int64, now time.Time, c *urlConfig) error {
	sh := t.shard(subject)
	n := now.UnixNano()
	sh.mu.Lock()
	defer sh.mu.Unlock()
	l := sh.subjects[subject]
	if l != nil {
		t.prune(l, n, c.idle)
		for _, s := range l.list {
			if s.sid != sid {
				continue
			}
			s.keep = max(s.keep, exp)
			if s.ended {
				return urlErrors[reasonSessionEnded]
			}
			s.last = n
			return nil
		}
		if len(l.list) == 0 {
			// Every session that holds a subject is counted in total,
			// so that the subjects too are bounded.
			delete(sh.subjects, subject)
			l = nil
		}
	}
	if c.maxSessions > 0 && l != nil {
		playing := 0
		var oldest *session
		for _, s := range l.list {
			if s.ended {
				continue
			}
			playing++
			if oldest == nil || s.start < oldest.start {
				oldest = s
			}
		}
		if playing >= c.maxSessions {
			if !c.endOldest {
				return urlErrors[reasonSessionLimit]
			}
			oldest.ended = true
		}
	}
	if t.total.Load() >= int64(c.tracked) {
		sh.mu.Unlock()
		t.sweep(n, c.idle)
		sh.mu.Lock()
		if t.total.Load() >= int64(c.tracked) {
			return ErrSessionsFull
		}
		l = sh.subjects[subject]
	}
	if l == nil {
		if sh.subjects == nil {
			sh.subjects = map[string]*sessionList{}
		}
		l = &sessionList{}
		sh.subjects[strings.Clone(subject)] = l
	}
	l.list = append(l.list, &session{sid: strings.Clone(sid), resource: strings.Clone(resource),
		address: strings.Clone(address), start: n, last: n, keep: exp})
	t.total.Add(1)
	return nil
}

// sweep drops every session that is over, and every subject left with
// none, at most once each sweepEvery. No shard's lock is held.
func (t *sessionTable) sweep(now int64, idle time.Duration) {
	last := t.swept.Load()
	if last != 0 && now-last < int64(sweepEvery) || !t.swept.CompareAndSwap(last, now) {
		return
	}
	for i := range t.shards {
		sh := &t.shards[i]
		sh.mu.Lock()
		for subject, l := range sh.subjects {
			if t.prune(l, now, idle); len(l.list) == 0 {
				delete(sh.subjects, subject)
			}
		}
		sh.mu.Unlock()
	}
}

// extend records that session sid of subject was handed a grant good until
// exp, so that an ended session stays refused as long as that grant lives.
func (t *sessionTable) extend(subject, sid string, exp int64) {
	sh := t.shard(subject)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if l := sh.subjects[subject]; l != nil {
		for _, s := range l.list {
			if s.sid == sid {
				s.keep = max(s.keep, exp)
			}
		}
	}
}

// Session is a playback session of a policy's signed URLs.
type Session struct {
	Subject  string    `json:"subject"`
	ID       string    `json:"id"`
	Resource string    `json:"resource"`
	Address  string    `json:"address"`
	Start    time.Time `json:"start"`
	Last     time.Time `json:"last"`
	Ended    bool      `json:"ended,omitempty"`
}

// Sessions returns the policy's playback sessions that are not over, by
// subject and start: those playing, and those ended whose grants still
// live.
func (p *Policy) Sessions() []Session {
	// A policy whose url_signing is gone shows only ended sessions.
	var idle time.Duration
	if st := p.state(); st != nil && st.url != nil {
		idle = st.url.idle
	}
	n := now().UnixNano()
	var out []Session
	for i := range p.sess.shards {
		sh := &p.sess.shards[i]
		sh.mu.Lock()
		for subject, l := range sh.subjects {
			for _, s := range l.list {
				if s.over(n, idle) {
					continue
				}
				out = append(out, Session{Subject: subject, ID: s.sid, Resource: s.resource, Address: s.address,
					Start: time.Unix(0, s.start), Last: time.Unix(0, s.last), Ended: s.ended})
			}
		}
		sh.mu.Unlock()
	}
	slices.SortFunc(out, func(a, b Session) int {
		if c := strings.Compare(a.Subject, b.Subject); c != 0 {
			return c
		}
		if c := a.Start.Compare(b.Start); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// EndSession ends session id of subject: its requests are refused 403 from
// now on, as are those of every grant it was handed, until they expire. It
// reports whether the policy had the session playing.
func (p *Policy) EndSession(subject, id string) bool {
	sh := p.sess.shard(subject)
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if l := sh.subjects[subject]; l != nil {
		for _, s := range l.list {
			if s.sid == id && !s.ended {
				s.ended = true
				return true
			}
		}
	}
	return false
}
