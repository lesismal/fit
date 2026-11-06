package linktoken

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/logs"
)

// ErrNotFound is the error of a token that is not the listener's.
var ErrNotFound = errors.New("linktoken: no such token")

// ErrBadRequest marks an error of what was asked: the pages and the Admin
// API answer it 400.
var ErrBadRequest = errors.New("bad request")

type badRequest struct{ msg string }

func (e *badRequest) Error() string        { return e.msg }
func (e *badRequest) Is(target error) bool { return target == ErrBadRequest }

func bad(format string, args ...any) error { return &badRequest{fmt.Sprintf(format, args...)} }

// Editor changes the listener's tokens in its member of the config with
// change, given the tokens as the config has them, and waits until the config
// changed has been applied, or ctx ends. An error of change is returned as it
// is.
type Editor func(ctx context.Context, change func(tokens []config.LinkToken) ([]config.LinkToken, error)) error

// Manager issues, lists, changes and revokes a listener's tokens, for its
// pages, the Admin API and the CLI alike. Every change is a config edit,
// applied as any page's change is, and logged to Log, the admin logger.
type Manager struct {
	// Role is the listener's role, as tunnel.hub or llm.node, for the log.
	Role string
	Reg  *Registry
	Edit Editor
	Log  *logs.Logger
}

// IssueReq asks for a token. ExpiresAt, in seconds since 1970, or TTL, a
// duration from now, set its expiry; neither, never.
type IssueReq struct {
	Name      string `json:"name"`
	Note      string `json:"note,omitempty"`
	ExpiresAt int64  `json:"expires_at,omitempty"`
	TTL       string `json:"ttl,omitempty"`
}

// Issued is a token made: Token is shown this once and kept nowhere.
type Issued struct {
	Token string `json:"token"`
	View
}

// ExpiryReq changes a token's expiry: ExpiresAt or TTL, as IssueReq's;
// neither, never.
type ExpiryReq struct {
	ExpiresAt int64  `json:"expires_at,omitempty"`
	TTL       string `json:"ttl,omitempty"`
}

// expiry is the expiry req asks for, from now.
func expiry(at int64, ttl string, now time.Time) (int64, error) {
	switch {
	case at != 0 && ttl != "":
		return 0, bad("give expires_at or ttl, not both")
	case at < 0:
		return 0, bad("expires_at is seconds since 1970, not negative")
	case at != 0 && at <= now.Unix():
		return 0, bad("expires_at is in the past")
	case ttl != "":
		d, err := ParseTTL(ttl)
		if err != nil {
			return 0, bad("%v", err)
		}
		return now.Add(d).Unix(), nil
	}
	return at, nil
}

// ParseTTL reads a token's lifetime: a Go duration, as 720h, or a whole
// number of days, as 30d.
func ParseTTL(s string) (time.Duration, error) {
	var d time.Duration
	var err error
	if n, ok := strings.CutSuffix(s, "d"); ok {
		var days int64
		days, err = strconv.ParseInt(n, 10, 32)
		d = time.Duration(days) * 24 * time.Hour
	} else {
		d, err = time.ParseDuration(s)
	}
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("ttl %q is not a positive duration, as 720h or 30d", s)
	}
	return d, nil
}

func (m *Manager) logf(format string, args ...any) {
	if m.Log != nil {
		m.Log.Infof(format, args...)
	}
}

func (m *Manager) now() time.Time { return m.Reg.now() }

// EditTimeout bounds a change of the tokens whose context has no deadline.
const EditTimeout = 30 * time.Second

// edit runs Edit, within EditTimeout unless ctx has a deadline.
func (m *Manager) edit(ctx context.Context, change func([]config.LinkToken) ([]config.LinkToken, error)) error {
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, EditTimeout)
		defer cancel()
	}
	return m.Edit(ctx, change)
}

// List is the tokens.
func (m *Manager) List() []View { return m.Reg.List() }

// Issue makes a token, adds it to the config, and returns it, its secret
// shown this once.
func (m *Manager) Issue(ctx context.Context, req IssueReq) (Issued, error) {
	name, note := strings.TrimSpace(req.Name), strings.TrimSpace(req.Note)
	if n := len([]rune(name)); n == 0 || n > 64 || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return Issued{}, bad("a token's name is 1 to 64 characters, without control characters")
	}
	if len(note) > 1024 {
		return Issued{}, bad("a token's note is up to 1024 bytes")
	}
	now := m.now()
	exp, err := expiry(req.ExpiresAt, req.TTL, now)
	if err != nil {
		return Issued{}, err
	}
	s := New()
	rec := config.LinkToken{ID: s.ID, Name: name, Hash: s.Hash(), CreatedAt: now.Unix(), ExpiresAt: exp, Note: note}
	err = m.edit(ctx, func(ts []config.LinkToken) ([]config.LinkToken, error) {
		if len(ts) >= config.MaxLinkTokens {
			return nil, bad("the listener has %d tokens, as many as it may: revoke one first", len(ts))
		}
		return append(ts, rec), nil
	})
	if err != nil {
		return Issued{}, err
	}
	m.logf("%s: issued link token %s (%q), %s", m.Role, s.ID, name, expiresText(exp))
	v, ok := m.Reg.Get(s.ID)
	if !ok {
		v = View{ID: s.ID, Name: name, Note: note, CreatedAt: rec.CreatedAt, ExpiresAt: exp, Members: []string{}, Links: []LinkView{}}
	}
	return Issued{Token: s.String(), View: v}, nil
}

func expiresText(at int64) string {
	if at == 0 {
		return "never expiring"
	}
	return "expiring at " + time.Unix(at, 0).UTC().Format(time.RFC3339)
}

// SetExpiry changes token id's expiry. An expiry now past closes its links.
func (m *Manager) SetExpiry(ctx context.Context, id string, req ExpiryReq) (View, error) {
	exp, err := expiry(req.ExpiresAt, req.TTL, m.now())
	if err != nil {
		return View{}, err
	}
	err = m.edit(ctx, func(ts []config.LinkToken) ([]config.LinkToken, error) {
		i := slices.IndexFunc(ts, func(t config.LinkToken) bool { return t.ID == id })
		if i < 0 {
			return nil, ErrNotFound
		}
		ts[i].ExpiresAt = exp
		return ts, nil
	})
	if err != nil {
		return View{}, err
	}
	m.logf("%s: link token %s now %s", m.Role, id, expiresText(exp))
	v, _ := m.Reg.Get(id)
	return v, nil
}

// Revoke removes token id from the config, which closes its links at once
// and frees its members.
func (m *Manager) Revoke(ctx context.Context, id string) error {
	err := m.edit(ctx, func(ts []config.LinkToken) ([]config.LinkToken, error) {
		i := slices.IndexFunc(ts, func(t config.LinkToken) bool { return t.ID == id })
		if i < 0 {
			return nil, ErrNotFound
		}
		return slices.Delete(ts, i, i+1), nil
	})
	if err != nil {
		return err
	}
	m.logf("%s: revoked link token %s", m.Role, id)
	return nil
}

// Bind adds member to token id's members in ts, as OnBind asks: ts unchanged
// when the token is gone or has it already. It is an edit's change.
func Bind(ts []config.LinkToken, id, member string) []config.LinkToken {
	i := slices.IndexFunc(ts, func(t config.LinkToken) bool { return t.ID == id })
	if i < 0 || slices.Contains(ts[i].Members, member) || len(ts[i].Members) >= config.MaxLinkTokenMembers {
		return ts
	}
	ts[i].Members = append(ts[i].Members, member)
	return ts
}
