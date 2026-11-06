package mcp

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/lesismal/fit/config"
)

// Section is the store's section the tokens are kept in.
const Section = "assistant.mcp"

// tokenPrefix starts every token, so that a leaked one is known for what it
// is; the token's ID and its secret follow, joined by '_'.
const tokenPrefix = "fitmcp_"

// The bounds of a token's fields.
const (
	maxTokenName  = 64
	maxTokenAllow = 16
)

// The levels a token may have: read (the read tools, the resources and the
// prompts) or propose (also the propose tools). edit adds only the page
// tools, which are not served, and apply is never a token's.
var tokenLevels = []string{config.AssistantRead, config.AssistantPropose}

// Token is an MCP token as the store keeps it: never the token itself, only
// a hash of its secret.
type Token struct {
	ID    string `json:"id"`
	User  string `json:"user"`
	Name  string `json:"name"`
	Level string `json:"level"`
	// Hash is the SHA-256 of the secret, in hex: the secret is 256 random
	// bits, so a hash that cannot be reversed is enough, and a check is
	// one hash.
	Hash    string `json:"hash"`
	Created int64  `json:"created"`
	Expires int64  `json:"expires"`
	// Allow, when not empty, narrows the addresses the token is used from,
	// within the settings' allow.
	Allow []string `json:"allow,omitempty"`
	// Conversation is the user's conversation the token's proposals are
	// kept in, once it proposed.
	Conversation string `json:"conversation,omitempty"`
	// LastUsed and LastAddr are when and where from it was last used, in
	// this process: they are kept when the tokens are next written.
	LastUsed int64  `json:"last_used,omitempty"`
	LastAddr string `json:"last_addr,omitempty"`
}

// expired reports whether t has expired at now, in Unix milliseconds.
func (t *Token) expired(now int64) bool { return now >= t.Expires }

// allows reports whether t may be used from addr.
func (t *Token) allows(addr netip.Addr) bool {
	if len(t.Allow) == 0 {
		return true
	}
	for _, s := range t.Allow {
		if p, ok := config.ParseAddrOrPrefix(s); ok && p.Contains(addr) {
			return true
		}
	}
	return false
}

// keptTokens is the store's section.
type keptTokens struct {
	Tokens []*Token `json:"tokens"`
}

// tokens are the tokens of every user, kept in the store, or in memory
// until fit restarts without one.
type tokens struct {
	mu   sync.Mutex
	list []*Token
	// save writes the tokens; nil without a store. It writes a file: on
	// the console's pool, under saveMu, so that two saves keep their
	// order.
	save   func(data []byte) error
	saveMu sync.Mutex
}

// load reads the tokens a section keeps; one that does not load is
// started again, and said so.
func (ts *tokens) load(sec []byte, logf func(string, ...any)) {
	if sec == nil {
		return
	}
	var k keptTokens
	if err := json.Unmarshal(sec, &k); err != nil {
		logf("assistant: the MCP tokens kept in the store do not load, and none are used: %v", err)
		return
	}
	for _, t := range k.Tokens {
		if t != nil && t.ID != "" && t.Hash != "" {
			ts.list = append(ts.list, t)
		}
	}
}

// write saves the tokens as they stand, the expired ones dropped. It
// writes a file: on the pool.
func (ts *tokens) write() error {
	if ts.save == nil {
		return nil
	}
	ts.saveMu.Lock()
	defer ts.saveMu.Unlock()
	ts.mu.Lock()
	now := time.Now().UnixMilli()
	ts.list = slices.DeleteFunc(ts.list, func(t *Token) bool { return t.expired(now - int64(expiredKept/time.Millisecond)) })
	k := keptTokens{Tokens: make([]*Token, len(ts.list))}
	for i, t := range ts.list {
		c := *t
		k.Tokens[i] = &c
	}
	ts.mu.Unlock()
	data, _ := json.Marshal(k)
	return ts.save(data)
}

// expiredKept is how long an expired token stays listed, so that its user
// sees why it is refused, before a write drops it.
const expiredKept = 7 * 24 * time.Hour

// newSecret returns a token's ID and secret, and the token as it is shown
// once.
func newSecret() (id, secret, raw string) {
	var b [32]byte
	_, _ = rand.Read(b[:])
	secret = base64.RawURLEncoding.EncodeToString(b[:])
	var i [8]byte
	_, _ = rand.Read(i[:])
	id = hex.EncodeToString(i[:])
	return id, secret, tokenPrefix + id + "_" + secret
}

func hashOf(secret string) string {
	h := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(h[:])
}

// The reasons a token is refused.
var (
	errNoToken  = errors.New("the request carries no MCP token: send Authorization: Bearer <token>, as the workbench's MCP page made it")
	errBadToken = errors.New("the MCP token is not valid: it was revoked, or never made here")
	errExpired  = errors.New("the MCP token has expired: make a new one on the workbench's MCP page")
)

// find returns a copy of the token raw is, or why it is refused.
func (ts *tokens) find(raw string, now int64) (*Token, error) {
	if raw == "" {
		return nil, errNoToken
	}
	rest, ok := strings.CutPrefix(raw, tokenPrefix)
	id, secret, ok2 := strings.Cut(rest, "_")
	if !ok || !ok2 {
		return nil, errBadToken
	}
	want := hashOf(secret)
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for _, t := range ts.list {
		if t.ID != id {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(t.Hash), []byte(want)) != 1 {
			return nil, errBadToken
		}
		if t.expired(now) {
			return nil, errExpired
		}
		c := *t
		return &c, nil
	}
	return nil, errBadToken
}

// used records that the token id was used, at now, from addr.
func (ts *tokens) used(id string, now int64, addr string) {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for _, t := range ts.list {
		if t.ID == id {
			t.LastUsed, t.LastAddr = now, addr
		}
	}
}

// setConversation keeps the conversation the token id's proposals are kept
// in, and reports whether it changed.
func (ts *tokens) setConversation(id, conv string) bool {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for _, t := range ts.list {
		if t.ID == id && t.Conversation != conv {
			t.Conversation = conv
			return true
		}
	}
	return false
}

// newToken is a token asked for on the MCP page.
type newToken struct {
	Name  string   `json:"name"`
	Level string   `json:"level"`
	Days  int      `json:"days"`
	Allow []string `json:"allow"`
}

// check returns what is wrong with n for user under m, or "".
func (n *newToken) check(m *config.AssistantMCP) string {
	n.Name = strings.TrimSpace(n.Name)
	switch {
	case n.Name == "" || len(n.Name) > maxTokenName || !utf8.ValidString(n.Name) ||
		strings.ContainsFunc(n.Name, func(r rune) bool { return r < ' ' || r == 0x7f }):
		return fmt.Sprintf("name is 1 to %d printable bytes", maxTokenName)
	case n.Level == "":
		n.Level = config.AssistantRead
	}
	switch {
	case !slices.Contains(tokenLevels, n.Level):
		return "level is read or propose"
	case n.Days < 1 || n.Days > m.TokenDays():
		return fmt.Sprintf("days is 1 to %d", m.TokenDays())
	case len(n.Allow) > maxTokenAllow:
		return fmt.Sprintf("at most %d addresses", maxTokenAllow)
	}
	for _, s := range n.Allow {
		if _, ok := config.ParseAddrOrPrefix(s); !ok {
			return fmt.Sprintf("%q is not an address, as 127.0.0.1, nor a prefix, as 10.0.0.0/8", s)
		}
	}
	return ""
}

// add makes the token n for user, at most max of the user's, and returns it
// and the token itself; status, not 0, refuses it.
func (ts *tokens) add(user string, n newToken, max int) (*Token, string, int, string) {
	id, secret, raw := newSecret()
	now := time.Now()
	t := &Token{ID: id, User: user, Name: n.Name, Level: n.Level, Hash: hashOf(secret), Created: now.UnixMilli(),
		Expires: now.Add(time.Duration(n.Days) * 24 * time.Hour).UnixMilli(), Allow: n.Allow}
	ts.mu.Lock()
	defer ts.mu.Unlock()
	mine := 0
	for _, o := range ts.list {
		if o.User != user {
			continue
		}
		if o.Name == n.Name {
			return nil, "", 409, "you have a token named " + n.Name + " already"
		}
		if !o.expired(now.UnixMilli()) {
			mine++
		}
	}
	if mine >= max {
		return nil, "", 409, fmt.Sprintf("you hold %d tokens, the most max_tokens_per_user lets you: revoke one first", mine)
	}
	ts.list = append(ts.list, t)
	c := *t
	return &c, raw, 0, ""
}

// remove revokes the token id, if by may: its own user, or anyone when
// anyone is set (a user who applies). It returns the token removed.
func (ts *tokens) remove(id, by string, anyone bool) *Token {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	for i, t := range ts.list {
		if t.ID == id && (t.User == by || anyone) {
			ts.list = slices.Delete(ts.list, i, i+1)
			return t
		}
	}
	return nil
}

// view returns the tokens of user, or of everyone when all is set, the
// latest first.
func (ts *tokens) view(user string, all bool) []Token {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	out := []Token{}
	for _, t := range ts.list {
		if all || t.User == user {
			c := *t
			c.Hash = ""
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(x, y Token) int { return int(y.Created - x.Created) })
	return out
}
