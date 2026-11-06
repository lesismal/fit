package config

import (
	"encoding/hex"
	"slices"
	"strings"
	"unicode"
)

// The bounds of a listener's link tokens (decision 035).
const (
	// MaxLinkTokens is the most tokens one listener issues.
	MaxLinkTokens = 1024
	// MaxLinkTokenMembers is the most members one token is bound to.
	MaxLinkTokenMembers = 256
	// LinkTokenPrefix starts every link token: the token is
	// LinkTokenPrefix, its ID, a dot and its secret in unpadded base64url.
	LinkTokenPrefix = "fitt1."
)

// LinkToken is a token a listener issued, which a node, proxy or hub links
// with in place of the shared password: the tunnel hub's, and an LLM role's
// listener's. Only a hash of its secret is kept; the token itself is shown
// once, when it is made.
type LinkToken struct {
	ID        string   `json:"id" fit:"required" doc:"The token's ID, 16 hex digits, the part of the token before its secret; unique among the listener's."`
	Name      string   `json:"name" fit:"required" doc:"What the token is for, as the pages list it: 1 to 64 characters."`
	Hash      string   `json:"hash" fit:"required" doc:"The SHA-256 of the key the token's secret derives, in hex: what the listener checks a link's proof with, which cannot be used to link. The token itself is not kept."`
	CreatedAt int64    `json:"created_at,omitempty" min:"0" doc:"When the token was made, in seconds since 1970."`
	ExpiresAt int64    `json:"expires_at,omitempty" min:"0" doc:"When the token expires, in seconds since 1970: it is refused from then on, and the links it holds are closed. Never when left out or 0."`
	Note      string   `json:"note,omitempty" doc:"Anything else about the token, for the pages; up to 1024 bytes."`
	Members   []string `json:"members,omitempty" doc:"The members that first linked with the token, which no other token may link as: a tunnel's node/<name> or proxy/<name>, an LLM role's ID. Written by the listener; revoking the token frees them."`

	At Value `json:"-" fit:"self"`
}

// Expired reports whether t has expired at now, in seconds since 1970.
func (t *LinkToken) Expired(now int64) bool { return t.ExpiresAt > 0 && now >= t.ExpiresAt }

// ValidLinkTokenID reports whether id is a token's ID: 16 lowercase hex
// digits.
func ValidLinkTokenID(id string) bool {
	if len(id) != 16 || strings.ToLower(id) != id {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

// CheckLinkTokenString checks the form of tok, a link token a dialer is
// given, and returns why it is not one, or "".
func CheckLinkTokenString(tok string) string {
	rest, ok := strings.CutPrefix(tok, LinkTokenPrefix)
	id, secret, dot := strings.Cut(rest, ".")
	if !ok || !dot || !ValidLinkTokenID(id) || len(secret) < 22 || len(secret) > 86 ||
		strings.IndexFunc(secret, func(r rune) bool {
			return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_')
		}) >= 0 {
		return "not a link token: one is " + LinkTokenPrefix + "<16 hex digits>.<secret>, as the listener's pages, its Admin API or fit token make it"
	}
	return ""
}

// checkLinkTokens checks a listener's tokens, at at.
func checkLinkTokens(tokens []LinkToken, at Value, add func(Value, string, ...any)) {
	if len(tokens) > MaxLinkTokens {
		add(at, "at most %d tokens", MaxLinkTokens)
	}
	members := map[string]string{}
	for i, t := range tokens {
		tat := at.Index(i)
		switch {
		case !ValidLinkTokenID(t.ID):
			add(tat.Field("id"), "the ID is 16 lowercase hex digits")
		case slices.IndexFunc(tokens, func(o LinkToken) bool { return o.ID == t.ID }) < i:
			add(tat.Field("id"), "another token has the ID %s", t.ID)
		}
		if n := len([]rune(t.Name)); n == 0 || n > 64 || strings.IndexFunc(t.Name, unicode.IsControl) >= 0 {
			add(tat.Field("name"), "the name is 1 to 64 characters, without control characters")
		}
		if h, err := hex.DecodeString(t.Hash); err != nil || len(h) != 32 {
			add(tat.Field("hash"), "the hash is 64 hex digits")
		}
		if len(t.Note) > 1024 {
			add(tat.Field("note"), "the note is up to 1024 bytes")
		}
		if len(t.Members) > MaxLinkTokenMembers {
			add(tat.Field("members"), "at most %d members", MaxLinkTokenMembers)
		}
		for j, m := range t.Members {
			if m == "" || len(m) > 256 || strings.IndexFunc(m, unicode.IsControl) >= 0 {
				add(tat.Field("members").Index(j), "a member is 1 to 256 bytes without control characters")
				continue
			}
			if other, ok := members[m]; ok {
				add(tat.Field("members").Index(j), "%s is bound to token %s already", m, other)
				continue
			}
			members[m] = t.ID
		}
	}
}
