package mediaauth

import (
	stdhttp "net/http"
	"net/netip"
)

// Whois finds who credentials stand for and what they may do, for a page
// that lists what its viewer may play rather than checking one stream: a
// token's subject and grant, or a user's name and grant once its password
// checks. done is called once, before Whois returns but for a bcrypt check,
// which runs on the package's pool as Check's does. Credentials that are
// empty give ErrNoCredentials; a nil policy, or one not loaded, gives
// ErrUnavailable.
func (p *Policy) Whois(c Credentials, done func(subject string, g Grant, err error)) {
	if p == nil {
		done("", Grant{}, ErrUnavailable)
		return
	}
	st := p.state()
	switch {
	case st == nil:
		done("", Grant{}, ErrUnavailable)
	case c.Token != "":
		done(p.token(st, c.Token))
	case c.User != "":
		p.Login(c.User, c.Password, done)
	default:
		done("", Grant{}, ErrNoCredentials)
	}
}

// ViewerAddr returns the address of the viewer r comes from, as a URL bound
// to an address is checked against it: the connection's peer or, behind
// url_signing's trusted_proxies, the last address in X-Forwarded-For that
// is not one of them; the peer for a nil policy. The zero Addr when r's
// peer is not an address.
func (p *Policy) ViewerAddr(r *stdhttp.Request) netip.Addr {
	var trusted []netip.Prefix
	if p != nil {
		if st := p.state(); st != nil && st.url != nil {
			trusted = st.url.trusted
		}
	}
	return clientIP(r, trusted)
}
