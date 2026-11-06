package config

import (
	"encoding/hex"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The defaults and bounds of MediaAuth.
const (
	DefaultMediaTokenTTL = 12 * time.Hour
	// MinMediaSecret is the fewest bytes a media_auth secret may have.
	MinMediaSecret = 16
	// MaxMediaKeys is the most keys a media_auth policy's keyring may hold.
	MaxMediaKeys = 256
)

// mediaKeyName is what a key of a media_auth keyring may be called: a token
// carries none, but a request and a revoke name it.
var mediaKeyName = regexp.MustCompile(`^[A-Za-z0-9._~-]{1,64}$`)

// MediaActions are what a media_auth policy grants: publishing a stream and
// playing it.
var MediaActions = []string{"publish", "play"}

// MediaAuth is a named policy of who may publish and play which streams,
// for the handlers and roles that take media: whip, whep, webrtc_proxy, and
// what refers to it by name with their auth param. See
// docs/decisions/030-media-auth-and-pages.md.
type MediaAuth struct {
	Name       string       `json:"name" fit:"required" doc:"The policy's name, which the auth param of whip, whep and webrtc_proxy, and the media roles, refer to it by. Letters, digits, '_', '.' and '-'."`
	Tokens     []MediaToken `json:"tokens,omitempty" doc:"Static bearer tokens, each with the streams it may publish and play: sent as Authorization: Bearer, a token query parameter or the fit_media_token cookie."`
	Users      []MediaUser  `json:"users,omitempty" doc:"Users with a password, each with the streams they may publish and play: sent as Authorization: Basic, as user and pass query parameters where a protocol has no header (RTMP, SRT), or RTSP's Digest. They also log in to fit's media pages, which hand out tokens signed with secret."`
	Secret     string       `json:"secret,omitempty" doc:"The key, at least 16 bytes, that tokens fit issues are signed with (HMAC-SHA256): fit media-token's and the media pages' logins. Tokens are good for this policy alone, until their expiry. When left out, a random key of the process's signs the pages' logins, which a restart forgets; fit media-token needs it. Or secret_file, one of the two, or keys."`
	SecretFile string       `json:"secret_file,omitempty" doc:"A file holding the secret tokens are signed with, read each time the config is applied: the handler media_tokens rotates it, and every token signed before is refused. At least 16 bytes, its line breaks at the end aside. Or secret, one of the two, or keys. None when left out: a random key of the process's signs, which a restart forgets."`
	Keys       []MediaKey   `json:"keys,omitempty" doc:"A keyring of signing keys, each a name and its secret or secret_file, in place of secret or secret_file: a token is signed with one key and checked against every key, so rotating one key, as the handler media_tokens does, refuses only the tokens that key signed and the others stay good. One of the two: keys, or secret/secret_file. None when left out."`
	Public     []string     `json:"public,omitempty" enum:"publish,play" doc:"The actions anyone may take without credentials, as [\"play\"] for streams anyone may watch and only some may publish. None when left out: every request needs credentials."`
	TokenTTL   Duration     `json:"token_ttl,omitempty" default:"\"12h\"" doc:"How long a token fit issues is good for, unless fit media-token's -ttl says otherwise. 12h when left out."`
	URLSigning *URLSigning  `json:"url_signing,omitempty" doc:"Turns on playback URLs that expire: URLs signed for one viewer and one resource (an item of vod, a stream of hls or httpflv) with the policy's secret or a key of its keys, as media_tokens' /urls issues them, and checked by those handlers on every playlist, segment and subtitle, at once and without a callback. Needs secret, secret_file or keys. Off when left out."`

	At Value `json:"-" fit:"self"`
}

// The defaults and bounds of URLSigning.
const (
	DefaultURLTTL          = 10 * time.Minute
	DefaultPlayGrace       = 30 * time.Minute
	DefaultSessionIdle     = 2 * time.Minute
	DefaultTrackedSessions = 100000
	// MaxURLOrigins is the most patterns referers and origins may list each.
	MaxURLOrigins = 256
)

// URLSigning is a media_auth policy's playback URLs that expire. See
// docs/media-library.md#playback-urls-that-expire.
type URLSigning struct {
	TTL                Duration `json:"ttl,omitempty" default:"\"10m\"" doc:"How long a URL media_tokens' /urls issues is good for when the request names no ttl, and at most the route's max_ttl. 10m when left out."`
	BindAddress        string   `json:"bind_address,omitempty" enum:"ip,subnet" default:"\"ip\"" doc:"How a URL issued for a viewer's address is bound to it: ip, to that address alone, or subnet, to its /24 for IPv4 and its /64 for IPv6, for viewers whose address changes within their network. A URL issued without an address is good from anywhere. ip when left out."`
	PlayGrace          Duration `json:"play_grace,omitempty" default:"\"30m\"" doc:"How long the grant a playlist hands its segments, init segments, keys and subtitles lives past the later of the URL's expiry and the item's end, played from when the playlist is fetched: so that a film does not stop halfway, nor after a pause, while the URL itself dies soon. A live stream's grant lives this long past the URL's expiry. 30m when left out."`
	Cookie             bool     `json:"cookie,omitempty" doc:"Hands the grant on as the cookie fit_media_grant, on the resource's path and HttpOnly, rather than in the query of every URI a playlist names, for players that drop a playlist's query parameters. The URL the player starts from keeps its query. Off when left out."`
	MaxSessions        int      `json:"max_sessions,omitempty" min:"0" doc:"The most playback sessions one subject may have at once, a session being a URL's sid, playing while it fetches within session_idle; past it, on_limit says what happens. No cap when left out or 0."`
	OnLimit            string   `json:"on_limit,omitempty" enum:"refuse,end_oldest" default:"\"refuse\"" doc:"What a new session past max_sessions does: refuse, answered 403, or end_oldest, which ends the subject's session that started first, whose next request is answered 403. refuse when left out."`
	SessionIdle        Duration `json:"session_idle,omitempty" default:"\"2m\"" doc:"How long a session may go without a request and still count as playing. 2m when left out."`
	MaxTrackedSessions int      `json:"max_tracked_sessions,omitempty" min:"0" default:"100000" doc:"The most sessions the policy keeps track of, of all its subjects; a new session past it is answered 503. 100000 when left out or 0."`
	Referers           []string `json:"referers,omitempty" doc:"The origins of the pages that may play the URLs, as https://www.example.com or https://*.example.com, * matching any run of characters, matched against the origin of a request's Referer: a request with another Referer is answered 403, one without any let through. Any when left out."`
	Origins            []string `json:"origins,omitempty" doc:"The origins whose scripts may fetch the URLs, as referers lists them, matched against a request's Origin: a request with another Origin is answered 403, one without any let through. Any when left out."`
	TrustedProxies     []string `json:"trusted_proxies,omitempty" doc:"The addresses (\"10.0.0.1\") and networks (\"10.0.0.0/8\") of proxies in front of fit, whose X-Forwarded-For is believed when a URL bound to an address is checked: the viewer is the last address in it that is not one of them. None when left out: the viewer is the connection's peer."`

	At Value `json:"-" fit:"self"`
}

// URLTTL returns ttl, or its default.
func (u *URLSigning) URLTTL() time.Duration { return durationOr(u.TTL, DefaultURLTTL) }

// Grace returns play_grace, or its default.
func (u *URLSigning) Grace() time.Duration { return durationOr(u.PlayGrace, DefaultPlayGrace) }

// Idle returns session_idle, or its default.
func (u *URLSigning) Idle() time.Duration { return durationOr(u.SessionIdle, DefaultSessionIdle) }

// Tracked returns max_tracked_sessions, or its default.
func (u *URLSigning) Tracked() int {
	if u.MaxTrackedSessions <= 0 {
		return DefaultTrackedSessions
	}
	return u.MaxTrackedSessions
}

// MediaKey is one key of a media_auth policy's keyring.
type MediaKey struct {
	Key        string `json:"key" fit:"required" doc:"The key's name: 1 to 64 letters, digits, '_', '.', '~' and '-', which a media_tokens request names to sign with it, and a revoke to rotate it."`
	Secret     string `json:"secret,omitempty" doc:"The key's secret, at least 16 bytes. Or secret_file, one of the two."`
	SecretFile string `json:"secret_file,omitempty" doc:"A file holding the key's secret, read each time the config is applied; the handler media_tokens rotates this file to refuse just the tokens this key signed. At least 16 bytes, its line breaks at the end aside. Or secret, one of the two."`

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
		if len(m.Tokens) == 0 && len(m.Users) == 0 && m.Secret == "" && m.SecretFile == "" && len(m.Keys) == 0 && len(m.Public) == 0 {
			add(m.At, "the policy lets no one in: set tokens, users, secret, secret_file, keys or public")
		}
		switch {
		case len(m.Keys) > 0 && (m.Secret != "" || m.SecretFile != ""):
			add(m.At.Field("keys"), "set keys, or secret or secret_file, one of the two")
		case m.Secret != "" && m.SecretFile != "":
			add(m.At.Field("secret_file"), "set secret or secret_file, one of the two")
		case m.Secret != "" && len(m.Secret) < MinMediaSecret:
			add(m.At.Field("secret"), "the secret is %d bytes: use at least %d", len(m.Secret), MinMediaSecret)
		}
		if len(m.Keys) > MaxMediaKeys {
			add(m.At.Field("keys"), "at most %d keys", MaxMediaKeys)
		}
		for j, k := range m.Keys {
			at := m.At.Field("keys").Index(j)
			switch {
			case !mediaKeyName.MatchString(k.Key):
				add(at.Field("key"), "%q is not a key name: 1 to 64 letters, digits, '.', '_', '~' and '-'", k.Key)
			case slices.IndexFunc(m.Keys, func(o MediaKey) bool { return o.Key == k.Key }) < j:
				add(at.Field("key"), "another key is already named %q", k.Key)
			}
			switch {
			case (k.Secret == "") == (k.SecretFile == ""):
				add(at, "set secret or secret_file, one of the two")
			case k.Secret != "" && len(k.Secret) < MinMediaSecret:
				add(at.Field("secret"), "the secret is %d bytes: use at least %d", len(k.Secret), MinMediaSecret)
			}
		}
		if u := m.URLSigning; u != nil {
			if m.Secret == "" && m.SecretFile == "" && len(m.Keys) == 0 {
				add(u.At, "url_signing needs a key to sign with: set secret, secret_file or keys")
			}
			checkURLSigning(u, add)
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

// originPattern is what referers and origins may list: http:// or
// https://, and a host, with * where any run of characters may be, and
// maybe a port.
var originPattern = regexp.MustCompile(`^https?://[A-Za-z0-9.*\[\]:-]{1,253}$`)

// ValidOriginPattern reports whether p is a pattern url_signing's referers
// and origins may list.
func ValidOriginPattern(p string) bool { return originPattern.MatchString(p) }

// checkURLSigning checks what url_signing's tags do not: its enums, minimums
// and durations are checked by them.
func checkURLSigning(u *URLSigning, add func(Value, string, ...any)) {
	for _, f := range []struct {
		name string
		list []string
	}{{"referers", u.Referers}, {"origins", u.Origins}} {
		if len(f.list) > MaxURLOrigins {
			add(u.At.Field(f.name), "at most %d origins", MaxURLOrigins)
		}
		for k, o := range f.list {
			if !ValidOriginPattern(o) {
				add(u.At.Field(f.name).Index(k), "%q is not an origin: http:// or https:// and a host, * matching any run of characters, as https://*.example.com", o)
			}
		}
	}
	for k, s := range u.TrustedProxies {
		if _, ok := ParseAddrOrPrefix(s); !ok {
			add(u.At.Field("trusted_proxies").Index(k), "%q is not an IP address or a network, as in \"10.0.0.1\" or \"10.0.0.0/8\"", s)
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
