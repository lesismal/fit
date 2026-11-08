package tracker

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/lesismal/fib"
	"github.com/lesismal/fib/bittorrent/metainfo"
	fibtracker "github.com/lesismal/fib/bittorrent/tracker"

	"github.com/lesismal/fit/access"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/mediaauth"
)

// newSecret returns a random key for the UDP connection IDs.
func newSecret() []byte {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return b
}

// allowList is the info hashes a private tracker tracks.
type allowList struct {
	set map[metainfo.InfoHash]struct{}
	// list keeps their order, for the statistics.
	list []metainfo.InfoHash
}

func (a *allowList) add(h metainfo.InfoHash) error {
	if _, ok := a.set[h]; ok {
		return nil
	}
	if len(a.list) >= config.MaxBTTrackerAllow {
		return fmt.Errorf("more than %d info hashes", config.MaxBTTrackerAllow)
	}
	a.set[h] = struct{}{}
	a.list = append(a.list, h)
	return nil
}

// loadAllow returns cfg's allow-list, its files read, or nil when it has
// none: every torrent is tracked. A line of a file that is not an info hash
// refuses the whole list, so that the list before stays in force.
func loadAllow(cfg *config.BitTorrentTracker) (*allowList, error) {
	if len(cfg.Allow) == 0 && len(cfg.AllowFiles) == 0 {
		return nil, nil
	}
	a := &allowList{set: map[metainfo.InfoHash]struct{}{}}
	for i, s := range cfg.Allow {
		h, ok := config.ParseInfoHash(s)
		if !ok {
			return nil, fmt.Errorf("allow[%d]: %q is not an info hash", i, s)
		}
		if err := a.add(h); err != nil {
			return nil, err
		}
	}
	for _, path := range cfg.AllowFiles {
		if err := a.readFile(path); err != nil {
			return nil, err
		}
	}
	return a, nil
}

// readFile adds the info hashes of the file at path: one on each line,
// blank lines and what follows a '#' left out.
func (a *allowList) readFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if st, err := f.Stat(); err == nil && st.Size() > access.MaxFileBytes {
		return fmt.Errorf("%s is %d bytes: at most %d", path, st.Size(), access.MaxFileBytes)
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 4096), 64<<10)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		h, ok := config.ParseInfoHash(line)
		if !ok {
			return fmt.Errorf("%s:%d: %q is not an info hash: 40 hex digits (v1) or 64 (v2)", path, n, line)
		}
		if err := a.add(h); err != nil {
			return fmt.Errorf("%s:%d: %w", path, n, err)
		}
	}
	if err := sc.Err(); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// allowed is fib's AllowInfoHash: whether the allow-list, if any, has h.
// It runs on a fib worker, and only looks h up.
func (r *Role) allowed(h metainfo.InfoHash) bool {
	a := r.allow.Load()
	if a == nil {
		return true
	}
	if _, ok := a.set[h]; ok {
		return true
	}
	r.refused[refusedUnregistered].Add(1)
	return false
}

// guard is the passkeys' policy, found by name: the role media_auth may
// apply after this role, so a policy not found yet is looked up again at
// each check until it is.
type guard struct {
	key  *fib.Engine
	name string
	p    *mediaauth.Policy
}

func (g *guard) policy() *mediaauth.Policy {
	if g.p != nil {
		return g.p
	}
	return mediaauth.Lookup(g.key, g.name)
}

// Stream returns the stream name a passkey's grant must play for a private
// tracker to serve the torrent h: bittorrent/<h in hex>.
func Stream(h metainfo.InfoHash) string { return "bittorrent/" + hex.EncodeToString(h[:]) }

// errNoPolicy refuses every request while the passkeys' policy is not
// loaded: a private tracker fails closed.
var errNoPolicy = errors.New("the tracker's passkeys are not available")

// authorize is fib's Authorize, asked about every announce and scrape the
// allow-list let through: a private tracker's passkey must play each of
// its torrents. It runs on a fib worker: the policy's checks are lookups,
// never a bcrypt check.
func (r *Role) authorize(a *fibtracker.Auth) error {
	if g := r.auth.Load(); g != nil {
		p := g.policy()
		if p == nil {
			r.refused[refusedPasskey].Add(1)
			return errNoPolicy
		}
		if g.p == nil {
			// Found at last: kept, so that it is not looked up again.
			r.auth.CompareAndSwap(g, &guard{key: g.key, name: g.name, p: p})
		}
		for _, h := range a.InfoHashes {
			if _, err := p.AllowPasskey(Stream(h), a.Passkey); err != nil {
				r.refused[refusedPasskey].Add(1)
				if errors.Is(err, mediaauth.ErrUnavailable) {
					// The policy kept was removed: look it up again next
					// time, a new one of the name may come.
					r.auth.CompareAndSwap(r.auth.Load(), &guard{key: g.key, name: g.name})
				}
				return passkeyError(err)
			}
		}
	}
	if a.Scrape {
		r.scrapes.Add(1)
	} else {
		r.announces.Add(1)
	}
	return nil
}

// passkeyError is the reason a client is told its passkey is refused.
func passkeyError(err error) error {
	switch {
	case errors.Is(err, mediaauth.ErrNoCredentials):
		return errors.New("a passkey is required")
	case errors.Is(err, mediaauth.ErrForbidden):
		return errors.New("the passkey may not use this torrent")
	case errors.Is(err, mediaauth.ErrBadCredentials):
		return errors.New("unknown passkey")
	}
	return errNoPolicy
}
