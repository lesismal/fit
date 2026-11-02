package config

import (
	"encoding/hex"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The defaults of MediaAuth.
const (
	DefaultMediaTokenTTL = 12 * time.Hour
	// MinMediaSecret is the fewest bytes a media_auth secret may have.
	MinMediaSecret = 16
)

// MediaActions are what a media_auth policy grants: publishing a stream and
// playing it.
var MediaActions = []string{"publish", "play"}

// MediaAuth is a named policy of who may publish and play which streams,
// for the handlers and roles that take media: whip, whep, webrtc_proxy, and
// what refers to it by name with their auth param. See
// docs/decisions/030-media-auth-and-pages.md.
type MediaAuth struct {
	Name     string       `json:"name" fit:"required" doc:"The policy's name, which the auth param of whip, whep and webrtc_proxy, and the media roles, refer to it by. Letters, digits, '_', '.' and '-'."`
	Tokens   []MediaToken `json:"tokens,omitempty" doc:"Static bearer tokens, each with the streams it may publish and play: sent as Authorization: Bearer, a token query parameter or the fit_media_token cookie."`
	Users    []MediaUser  `json:"users,omitempty" doc:"Users with a password, each with the streams they may publish and play: sent as Authorization: Basic, as user and pass query parameters where a protocol has no header (RTMP, SRT), or RTSP's Digest. They also log in to fit's media pages, which hand out tokens signed with secret."`
	Secret   string       `json:"secret,omitempty" doc:"The key, at least 16 bytes, that tokens fit issues are signed with (HMAC-SHA256): fit media-token's and the media pages' logins. Tokens are good for this policy alone, until their expiry. When left out, a random key of the process's signs the pages' logins, which a restart forgets; fit media-token needs it."`
	Public   []string     `json:"public,omitempty" enum:"publish,play" doc:"The actions anyone may take without credentials, as [\"play\"] for streams anyone may watch and only some may publish. None when left out: every request needs credentials."`
	TokenTTL Duration     `json:"token_ttl,omitempty" default:"\"12h\"" doc:"How long a token fit issues is good for, unless fit media-token's -ttl says otherwise. 12h when left out."`

	At Value `json:"-" fit:"self"`
}

// MediaToken is a static bearer token of a media_auth policy.
type MediaToken struct {
	Name        string   `json:"name,omitempty" doc:"What the token is called in logs, which never show the token itself: token:<name>, or token when left out."`
	Token       string   `json:"token,omitempty" doc:"The token, at least 16 characters. Or token_sha256, one of the two."`
	TokenSHA256 string   `json:"token_sha256,omitempty" doc:"Instead of the token, its SHA-256 in hex, so that the config does not hold it, as sha256sum prints it for the token alone, without a newline: printf %s \"$TOKEN\" | sha256sum."`
	Publish     []string `json:"publish,omitempty" doc:"The streams the token may publish, by name or path, with * matching any run of characters, '/' among them, as live/* or room1-*. None when left out."`
	Play        []string `json:"play,omitempty" doc:"The streams the token may play, as publish lists them. None when left out."`

	At Value `json:"-" fit:"self"`
}

// MediaUser is a user of a media_auth policy.
type MediaUser struct {
	Name         string   `json:"name" fit:"required" doc:"The user's name; it may not contain a colon or a control character."`
	Password     string   `json:"password,omitempty" doc:"The user's password. A user with one can also log in with RTSP's Digest, which needs the password itself. Or password_hash, one of the two."`
	PasswordHash string   `json:"password_hash,omitempty" doc:"Instead of the password, its bcrypt hash, as fit hash-password makes it: Basic and the query's user and pass only, checked off fib's workers. A password once found right is remembered, as a keyed SHA-256, so that the next check is at once."`
	Publish      []string `json:"publish,omitempty" doc:"The streams the user may publish, as a token's publish lists them. None when left out."`
	Play         []string `json:"play,omitempty" doc:"The streams the user may play. None when left out."`

	At Value `json:"-" fit:"self"`
}

// TTL returns token_ttl, or its default.
func (m *MediaAuth) TTL() time.Duration { return durationOr(m.TokenTTL, DefaultMediaTokenTTL) }

// HashBytes returns t's token_sha256 decoded, or nil when it is not 64 hex
// digits.
func (t MediaToken) HashBytes() []byte {
	b, err := hex.DecodeString(t.TokenSHA256)
	if err != nil || len(b) != 32 {
		return nil
	}
	return b
}

// mediaPattern is what a stream pattern may hold: a stream's name or path,
// and *.
var mediaPattern = regexp.MustCompile(`^[A-Za-z0-9._~/*-]{1,256}$`)

// ValidMediaPattern reports whether p is a stream pattern of media_auth: 1
// to 256 letters, digits, '.', '_', '~', '-', '/' and '*'.
func ValidMediaPattern(p string) bool { return mediaPattern.MatchString(p) }

// MediaAuthPolicy returns the policy of c named name, or nil.
func (c *Config) MediaAuthPolicy(name string) *MediaAuth {
	for i := range c.MediaAuth {
		if c.MediaAuth[i].Name == name {
			return &c.MediaAuth[i]
		}
	}
	return nil
}

func (c *Config) checkMediaAuth(add func(Value, string, ...any)) {
	for i := range c.MediaAuth {
		m := &c.MediaAuth[i]
		switch {
		case !namePattern.MatchString(m.Name):
			add(m.At.Field("name"), "%q is not a valid name: use letters, digits, '_', '.' and '-', starting with a letter or digit", m.Name)
		case slices.IndexFunc(c.MediaAuth, func(o MediaAuth) bool { return o.Name == m.Name }) < i:
			add(m.At.Field("name"), "another media_auth policy is already named %q", m.Name)
		}
		if len(m.Tokens) == 0 && len(m.Users) == 0 && m.Secret == "" && len(m.Public) == 0 {
			add(m.At, "the policy lets no one in: set tokens, users, secret or public")
		}
		if m.Secret != "" && len(m.Secret) < MinMediaSecret {
			add(m.At.Field("secret"), "the secret is %d bytes: use at least %d", len(m.Secret), MinMediaSecret)
		}
		for j, a := range m.Public {
			if slices.Index(m.Public, a) < j {
				add(m.At.Field("public").Index(j), "%q is listed twice", a)
			}
		}
		for j, t := range m.Tokens {
			at := m.At.Field("tokens").Index(j)
			switch {
			case (t.Token == "") == (t.TokenSHA256 == ""):
				add(at, "set token or token_sha256, one of the two")
			case t.Token != "" && len(t.Token) < 16:
				add(at.Field("token"), "the token is %d characters: use at least 16", len(t.Token))
			case t.TokenSHA256 != "" && t.HashBytes() == nil:
				add(at.Field("token_sha256"), "not a SHA-256 in hex: 64 hex digits")
			}
			if t.Name != "" && slices.IndexFunc(m.Tokens, func(o MediaToken) bool { return o.Name == t.Name }) < j {
				add(at.Field("name"), "another token is already named %q", t.Name)
			}
			checkMediaPatterns(at, t.Publish, t.Play, add)
		}
		for j, u := range m.Users {
			at := m.At.Field("users").Index(j)
			switch {
			case u.Name == "" || strings.ContainsAny(u.Name, ":\x00\r\n\t"):
				add(at.Field("name"), "%q is not a user name: it is empty, or has a colon or a control character", u.Name)
			case slices.IndexFunc(m.Users, func(o MediaUser) bool { return o.Name == u.Name }) < j:
				add(at.Field("name"), "another user is already named %q", u.Name)
			}
			switch {
			case (u.Password == "") == (u.PasswordHash == ""):
				add(at, "set password or password_hash, one of the two")
			case u.PasswordHash != "" && !strings.HasPrefix(u.PasswordHash, "$2"):
				add(at.Field("password_hash"), "not a bcrypt hash: make one with fit hash-password")
			}
			checkMediaPatterns(at, u.Publish, u.Play, add)
		}
	}
}

func checkMediaPatterns(at Value, publish, play []string, add func(Value, string, ...any)) {
	for _, f := range []struct {
		name string
		list []string
	}{{"publish", publish}, {"play", play}} {
		for k, p := range f.list {
			if !ValidMediaPattern(p) {
				add(at.Field(f.name).Index(k), "%q is not a stream pattern: 1 to 256 letters, digits, '.', '_', '~', '-', '/' and '*'", p)
			}
		}
	}
}
