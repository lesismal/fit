// Package auth has the middleware that authenticate requests: basic_auth,
// users and bcrypt-hashed passwords, and client_cert, the certificate a
// client sent to a listener with tls.client_auth. Who a request was
// authenticated as goes into its access log entry, as observe.Entry.User,
// where later middleware find it too.
package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	stdhttp "net/http"
	"strings"
	"sync/atomic"

	fibhttp "github.com/lesismal/fib/http"
	"golang.org/x/crypto/bcrypt"

	"github.com/lesismal/fit/module"
	"github.com/lesismal/fit/observe"
)

func init() {
	module.Register(module.Info{ID: module.Middlewares + ".basic_auth", New: func() any { return new(Basic) }, Doc: "Asks for a user name and password (Basic authentication), checked against bcrypt hashes."})
	module.Register(module.Info{ID: module.Middlewares + ".client_cert", New: func() any { return new(ClientCert) }, Doc: "Asks for a client certificate, verified by the listener's TLS (mTLS), and passes its subject on."})
}

// Basic asks for a user and password with HTTP Basic authentication and
// checks them against bcrypt hashes. bcrypt takes tens of milliseconds of
// CPU a check, which on a fib worker would hold up every connection it
// serves, so checks run on a small pool of their own while the request waits
// retained; a password that passed is remembered, as a keyed SHA-256, so
// that the same user's next requests are let through at once.
type Basic struct {
	Realm                string `json:"realm,omitempty" default:"\"fit\"" doc:"The realm the browser shows when it asks for a user and password; fit when left out."`
	Users                []User `json:"users" fit:"required" doc:"The users allowed in, with their passwords' bcrypt hashes."`
	UserHeader           string `json:"user_header,omitempty" doc:"A request field to set to the user's name for the handler, and the upstream a proxy sends the request to; any the client sent is replaced."`
	ForwardAuthorization bool   `json:"forward_authorization,omitempty" doc:"Leave the Authorization field, with the password, on the request the handler sees; it is removed when left out."`

	accounts   map[string]*account
	decoy      []byte // a hash an unknown user's password is checked against
	key        [32]byte
	challenge  []string // the WWW-Authenticate value
	userHeader string   // canonical
	graph      module.Holder
}

// User is one user basic_auth lets in.
type User struct {
	Username     string `json:"username" fit:"required" doc:"The user's name; it may not contain a colon."`
	PasswordHash string `json:"password_hash" fit:"required" doc:"The bcrypt hash of the user's password, as fit hash-password or htpasswd -B makes it."`
}

// account is a user, and the password last found to be theirs.
type account struct {
	hash []byte
	good atomic.Pointer[[sha256.Size]byte]
}

// Provision checks the users and the realm.
func (ba *Basic) Provision(ctx *module.Context) error {
	if ba.Realm == "" {
		ba.Realm = "fit"
	}
	for i := range len(ba.Realm) {
		if c := ba.Realm[i]; c < 0x20 || c == 0x7f || c == '"' || c == '\\' {
			return ctx.Errorf("realm", "%q has a quote, a backslash or a control character", ba.Realm)
		}
	}
	if len(ba.Users) == 0 {
		return ctx.Errorf("users", "at least one user is needed")
	}
	ba.accounts = make(map[string]*account, len(ba.Users))
	for i, u := range ba.Users {
		switch {
		case u.Username == "" || strings.ContainsAny(u.Username, ":\x00\r\n"):
			return ctx.Errorf("users", "user %d: %q is not a user name: it is empty, or has a colon or a control character", i, u.Username)
		case ba.accounts[u.Username] != nil:
			return ctx.Errorf("users", "user %d: %q is listed twice", i, u.Username)
		}
		if _, err := bcrypt.Cost([]byte(u.PasswordHash)); err != nil {
			return ctx.Errorf("users", "user %d: password_hash is not a bcrypt hash: %v", i, err)
		}
		ba.accounts[u.Username] = &account{hash: []byte(u.PasswordHash)}
	}
	ba.decoy = []byte(ba.Users[0].PasswordHash)
	if ba.UserHeader != "" {
		if !validName(ba.UserHeader) {
			return ctx.Errorf("user_header", "%q is not a field name", ba.UserHeader)
		}
		ba.userHeader = stdhttp.CanonicalHeaderKey(ba.UserHeader)
	}
	ba.challenge = []string{`Basic realm="` + ba.Realm + `", charset="UTF-8"`}
	_, _ = rand.Read(ba.key[:])
	ba.graph = ctx.Graph()
	return nil
}

// Wrap returns next behind the check.
func (ba *Basic) Wrap(next module.Handler) module.Handler {
	return fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		user, password, ok := c.Request.BasicAuth()
		if !ok {
			ba.refuse(c)
			return
		}
		a := ba.accounts[user]
		if a != nil && a.remembers(ba.mac(password)) {
			ba.admit(c, user, next)
			return
		}
		// The request waits for its check retained, and holds the config
		// until the check, and next if it passes, are done.
		k := &check{ba: ba, c: c, next: next, account: a, user: user, password: password}
		ba.graph.Retain()
		k.run = c.Async(k.verify)
		if !checks.submit(k) {
			k.busy = true
			k.run()
		}
	})
}

// mac is what an account remembers of a password: a SHA-256 keyed with a
// secret of the middleware's, so that what is kept in memory is no hash
// anyone could check passwords against.
func (ba *Basic) mac(password string) [sha256.Size]byte {
	var stack [128]byte
	b := append(append(stack[:0], ba.key[:]...), password...)
	return sha256.Sum256(b)
}

// remembers reports whether mac is the password last found to be a's.
func (a *account) remembers(mac [sha256.Size]byte) bool {
	good := a.good.Load()
	return good != nil && subtle.ConstantTimeCompare(good[:], mac[:]) == 1
}

// admit lets the request through as user.
func (ba *Basic) admit(c *fibhttp.Context, user string, next module.Handler) {
	if !ba.ForwardAuthorization {
		delete(c.Request.Header, "Authorization")
	}
	if ba.userHeader != "" {
		c.Request.Header[ba.userHeader] = []string{user}
	}
	setUser(c, user)
	next.ServeHTTP(c)
}

// refuse answers 401 Unauthorized, asking for a user and password.
func (ba *Basic) refuse(c *fibhttp.Context) {
	// A header of its own for each response: hooks may change it.
	_ = c.WriteResponse(fibhttp.Response{
		StatusCode: stdhttp.StatusUnauthorized,
		Header: stdhttp.Header{
			"Www-Authenticate": ba.challenge,
			"Content-Type":     {"text/plain; charset=utf-8"},
		},
		Body: unauthorized,
	})
}

var unauthorized = []byte("Unauthorized\n")

// setUser records who the request was authenticated as.
func setUser(c *fibhttp.Context, user string) {
	e := observe.EntryOf(c)
	if e == nil {
		e = observe.Track(c)
	}
	e.User = user
}

// UserOf returns who the request c serves was authenticated as, by
// basic_auth or client_cert, or "" when it was not.
func UserOf(c *fibhttp.Context) string {
	if e := observe.EntryOf(c); e != nil {
		return e.User
	}
	return ""
}

// validName reports whether name is an HTTP token.
func validName(name string) bool {
	if name == "" {
		return false
	}
	for i := range len(name) {
		c := name[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' {
			continue
		}
		if !strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c)) {
			return false
		}
	}
	return true
}
