package linktoken

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/lesismal/fit/config"
)

// MaxSweep is the longest a registry waits before it looks again for tokens
// that expired: a token's links are closed at its expiry, or at most this
// late when the wall clock jumped.
const MaxSweep = time.Minute

// The errors of a token a link presents.
var (
	ErrUnknown = errors.New("linktoken: no such token: it was never issued here, or it was revoked")
	ErrExpired = errors.New("linktoken: the token has expired")
	ErrBound   = errors.New("linktoken: the member is bound to another token")
	ErrFull    = fmt.Errorf("linktoken: the token is bound to %d members, as many as it may be", config.MaxLinkTokenMembers)
)

// Registry is a listener's tokens, as its config lists them, and the links
// that use them. It is safe for concurrent use; nothing of it blocks.
type Registry struct {
	// OnBind, if set, runs, off the registry's lock, when a member links
	// with a token for the first time, so that the binding is written into
	// the config.
	OnBind func(token, member string)
	// Now, if set, is the time; tests set it.
	Now func() time.Time

	mu      sync.Mutex
	tokens  map[string]*entry
	bound   map[string]string
	timer   *time.Timer
	stopped bool
}

type entry struct {
	rec      config.LinkToken
	stored   []byte
	lastUse  time.Time
	lastAddr string
	uses     map[*Use]struct{}
}

// Use is a link that presented a token: Release it once the link closes.
type Use struct {
	r      *Registry
	token  string
	Member string
	Addr   string
	Since  time.Time
	close  func()
}

// Token is the ID of the token the link presented.
func (u *Use) Token() string { return u.token }

// Release forgets the link.
func (u *Use) Release() {
	if u == nil {
		return
	}
	u.r.mu.Lock()
	if e := u.r.tokens[u.token]; e != nil {
		delete(e.uses, u)
	}
	u.r.mu.Unlock()
}

func (r *Registry) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// Set moves the registry to recs, the listener's tokens as its config has
// them. The links of a token that is gone, whose hash changed, or that has
// expired are closed, at once and off the lock; the bindings of a token that
// is gone are forgotten. A binding not yet in the config is kept while its
// token is.
func (r *Registry) Set(recs []config.LinkToken) {
	r.mu.Lock()
	var closing []*Use
	next := make(map[string]*entry, len(recs))
	bound := map[string]string{}
	now := r.now().Unix()
	for _, rec := range recs {
		stored, err := StoredOf(rec.Hash)
		if err != nil {
			continue // config validation refuses it
		}
		e := r.tokens[rec.ID]
		if e != nil && rec.Hash == e.rec.Hash {
			e.rec = rec
		} else {
			if e != nil {
				for u := range e.uses {
					closing = append(closing, u)
				}
			}
			e = &entry{rec: rec, stored: stored, uses: map[*Use]struct{}{}}
		}
		if rec.Expired(now) {
			for u := range e.uses {
				closing = append(closing, u)
			}
			clear(e.uses)
		}
		next[rec.ID] = e
		for _, m := range rec.Members {
			bound[m] = rec.ID
		}
	}
	for id, e := range r.tokens {
		if _, ok := next[id]; !ok {
			for u := range e.uses {
				closing = append(closing, u)
			}
		}
	}
	for m, id := range r.bound {
		if _, ok := bound[m]; !ok && next[id] != nil {
			bound[m] = id
		}
	}
	r.tokens, r.bound = next, bound
	r.scheduleLocked()
	r.mu.Unlock()
	closeUses(closing)
}

func closeUses(us []*Use) {
	for _, u := range us {
		if u.close != nil {
			u.close()
		}
	}
}

// scheduleLocked sets the timer for the next expiry. mu is held.
func (r *Registry) scheduleLocked() {
	if r.stopped {
		return
	}
	now := r.now()
	var next time.Time
	for _, e := range r.tokens {
		if at := time.Unix(e.rec.ExpiresAt, 0); e.rec.ExpiresAt > 0 && at.After(now) && (next.IsZero() || at.Before(next)) {
			next = at
		}
	}
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	if next.IsZero() {
		return
	}
	r.timer = time.AfterFunc(min(next.Sub(now), MaxSweep), r.sweep)
}

// sweep closes the links of the tokens that have expired.
func (r *Registry) sweep() {
	r.mu.Lock()
	now := r.now().Unix()
	var closing []*Use
	for _, e := range r.tokens {
		if e.rec.Expired(now) {
			for u := range e.uses {
				closing = append(closing, u)
			}
			clear(e.uses)
		}
	}
	r.scheduleLocked()
	r.mu.Unlock()
	closeUses(closing)
}

// Stop stops the registry's timer and closes nothing: the role closes its
// links itself.
func (r *Registry) Stop() {
	r.mu.Lock()
	r.stopped = true
	if r.timer != nil {
		r.timer.Stop()
		r.timer = nil
	}
	r.mu.Unlock()
}

// Lookup is the stored key of token id, for a handshake to check a proof
// with, or why the token is refused.
func (r *Registry) Lookup(id string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.tokens[id]
	switch {
	case e == nil:
		return nil, ErrUnknown
	case e.rec.Expired(r.now().Unix()):
		return nil, ErrExpired
	}
	return e.stored, nil
}

// Len is how many tokens the registry has.
func (r *Registry) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.tokens)
}

// Admit records a link, from addr, of member that proved token id: closing
// it closes the link when the token is revoked or expires. It refuses a
// token that is gone or has expired, and a member bound to another token;
// a member's first link binds it to the token.
func (r *Registry) Admit(id, member, addr string, closing func()) (*Use, error) {
	r.mu.Lock()
	e := r.tokens[id]
	now := r.now()
	switch {
	case e == nil:
		r.mu.Unlock()
		return nil, ErrUnknown
	case e.rec.Expired(now.Unix()):
		r.mu.Unlock()
		return nil, ErrExpired
	}
	have, ok := r.bound[member]
	if ok && have != id {
		r.mu.Unlock()
		return nil, fmt.Errorf("%w: %s first linked with token %s", ErrBound, member, have)
	}
	if !ok {
		if r.membersLocked(id) >= config.MaxLinkTokenMembers {
			r.mu.Unlock()
			return nil, ErrFull
		}
		r.bound[member] = id
	}
	u := &Use{r: r, token: id, Member: member, Addr: addr, Since: now, close: closing}
	e.uses[u] = struct{}{}
	e.lastUse, e.lastAddr = now, addr
	bind := r.OnBind
	r.mu.Unlock()
	if !ok && bind != nil {
		bind(id, member)
	}
	return u, nil
}

// membersLocked is how many members are bound to token id. mu is held.
func (r *Registry) membersLocked(id string) int {
	n := 0
	for _, t := range r.bound {
		if t == id {
			n++
		}
	}
	return n
}

// View is a token as the pages, the Admin API and the CLI list it: never
// its hash.
type View struct {
	ID        string     `json:"id"`
	Name      string     `json:"name"`
	Note      string     `json:"note,omitempty"`
	CreatedAt int64      `json:"created_at,omitempty"`
	ExpiresAt int64      `json:"expires_at,omitempty"`
	Expired   bool       `json:"expired"`
	LastUsed  int64      `json:"last_used,omitempty"`
	LastAddr  string     `json:"last_addr,omitempty"`
	Members   []string   `json:"members"`
	Links     []LinkView `json:"links"`
}

// LinkView is a link that uses a token.
type LinkView struct {
	Member string `json:"member"`
	Addr   string `json:"addr"`
	Since  int64  `json:"since"`
}

// List is the tokens, by name and then ID, each with its last use since
// fit started, the members bound to it and the links that use it.
func (r *Registry) List() []View {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().Unix()
	out := make([]View, 0, len(r.tokens))
	for id, e := range r.tokens {
		v := r.viewLocked(id, e, now)
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Get is token id's view, or false.
func (r *Registry) Get(id string) (View, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.tokens[id]
	if e == nil {
		return View{}, false
	}
	return r.viewLocked(id, e, r.now().Unix()), true
}

func (r *Registry) viewLocked(id string, e *entry, now int64) View {
	v := View{ID: id, Name: e.rec.Name, Note: e.rec.Note, CreatedAt: e.rec.CreatedAt, ExpiresAt: e.rec.ExpiresAt,
		Expired: e.rec.Expired(now), LastAddr: e.lastAddr, Members: []string{}, Links: []LinkView{}}
	if !e.lastUse.IsZero() {
		v.LastUsed = e.lastUse.Unix()
	}
	for m, t := range r.bound {
		if t == id {
			v.Members = append(v.Members, m)
		}
	}
	slices.Sort(v.Members)
	for u := range e.uses {
		v.Links = append(v.Links, LinkView{Member: u.Member, Addr: u.Addr, Since: u.Since.Unix()})
	}
	sort.Slice(v.Links, func(i, j int) bool {
		a, b := v.Links[i], v.Links[j]
		if a.Member != b.Member {
			return a.Member < b.Member
		}
		return a.Addr < b.Addr
	})
	return v
}
