package router

import (
	"fmt"
	stdhttp "net/http"
	"net/netip"
	"regexp"
	"strings"
)

// Methods matches a request whose method is one of methods, which are
// compared as written: methods are case-sensitive.
func Methods(methods ...string) Matcher { return methodMatcher(methods) }

type methodMatcher []string

func (m methodMatcher) Match(r *stdhttp.Request) bool {
	for _, method := range m {
		if r.Method == method {
			return true
		}
	}
	return false
}

// Headers matches a request that has every header named in h, with, for a
// name given values, one of them as the value of one of its fields of that
// name. Names ignore case; values do not.
func Headers(h map[string][]string) Matcher {
	m := make(fieldsMatcher, 0, len(h))
	for name, values := range h {
		m = append(m, field{name: stdhttp.CanonicalHeaderKey(name), values: values})
	}
	return m
}

type field struct {
	name   string
	values []string
}

type fieldsMatcher []field

func (m fieldsMatcher) Match(r *stdhttp.Request) bool {
	for _, f := range m {
		got, ok := r.Header[f.name]
		if !ok || len(f.values) > 0 && !anyOf(got, f.values) {
			return false
		}
	}
	return true
}

func anyOf(got, want []string) bool {
	for _, g := range got {
		for _, w := range want {
			if g == w {
				return true
			}
		}
	}
	return false
}

// Query matches a request whose query has every key in q, with, for a key
// given values, one of them as one of its values. Keys and values are
// compared once unescaped, without allocating.
func Query(q map[string][]string) Matcher {
	m := make(queryMatcher, 0, len(q))
	for key, values := range q {
		m = append(m, field{name: key, values: values})
	}
	return m
}

type queryMatcher []field

func (m queryMatcher) Match(r *stdhttp.Request) bool {
	for _, f := range m {
		if !queryHas(r.URL.RawQuery, f.name, f.values) {
			return false
		}
	}
	return true
}

// queryHas reports whether raw, a URL's escaped query, has key, with one of
// values when there are any.
func queryHas(raw, key string, values []string) bool {
	for raw != "" {
		var pair string
		pair, raw, _ = strings.Cut(raw, "&")
		k, v, _ := strings.Cut(pair, "=")
		if !unescapedIs(k, key) {
			continue
		}
		if len(values) == 0 {
			return true
		}
		for _, want := range values {
			if unescapedIs(v, want) {
				return true
			}
		}
	}
	return false
}

// unescapedIs reports whether s, query-escaped, unescapes to want; an
// invalid escape matches nothing.
func unescapedIs(s, want string) bool {
	j := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '+':
			c = ' '
		case '%':
			if i+2 >= len(s) {
				return false
			}
			hi, ok1 := unhex(s[i+1])
			lo, ok2 := unhex(s[i+2])
			if !ok1 || !ok2 {
				return false
			}
			c = hi<<4 | lo
			i += 2
		}
		if j >= len(want) || want[j] != c {
			return false
		}
		j++
	}
	return j == len(want)
}

func unhex(c byte) (byte, bool) {
	switch {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, true
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// RemoteIP matches a request from an address in one of nets, each an IP
// address or a CIDR prefix, as in "10.0.0.0/8". The address is the
// connection's peer, r.RemoteAddr; an IPv4 address mapped into IPv6 counts as
// IPv4.
func RemoteIP(nets ...string) (Matcher, error) {
	m := make(ipMatcher, len(nets))
	for i, s := range nets {
		p, err := ParsePrefix(s)
		if err != nil {
			return nil, err
		}
		m[i] = p
	}
	return m, nil
}

// ParsePrefix parses an IP address, as a prefix of its full length, or a
// CIDR prefix.
func ParsePrefix(s string) (netip.Prefix, error) {
	if strings.Contains(s, "/") {
		p, err := netip.ParsePrefix(s)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("%q is not an IP address or a CIDR prefix", s)
		}
		if p.Addr().Is4In6() {
			return netip.Prefix{}, fmt.Errorf("%q is an IPv4 prefix written in IPv6: write it in IPv4", s)
		}
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil || a.Zone() != "" {
		return netip.Prefix{}, fmt.Errorf("%q is not an IP address or a CIDR prefix", s)
	}
	a = a.Unmap()
	return netip.PrefixFrom(a, a.BitLen()), nil
}

type ipMatcher []netip.Prefix

func (m ipMatcher) Match(r *stdhttp.Request) bool {
	a, ok := remoteAddr(r.RemoteAddr)
	if !ok {
		return false
	}
	for _, p := range m {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// remoteAddr parses the IP address of a RemoteAddr, "ip:port" or a bare IP.
func remoteAddr(s string) (netip.Addr, bool) {
	if ap, err := netip.ParseAddrPort(s); err == nil {
		return ap.Addr().Unmap().WithZone(""), true
	}
	a, err := netip.ParseAddr(s)
	return a.Unmap().WithZone(""), err == nil
}

// PathRegexp matches a request whose path, r.URL.Path, one of exprs matches,
// anywhere in it unless anchored with ^ and $; exprs use Go's syntax.
func PathRegexp(exprs ...string) (Matcher, error) {
	m := make(regexpMatcher, len(exprs))
	for i, s := range exprs {
		re, err := regexp.Compile(s)
		if err != nil {
			return nil, fmt.Errorf("%q is not a valid regular expression: %v", s, err)
		}
		m[i] = re
	}
	return m, nil
}

type regexpMatcher []*regexp.Regexp

func (m regexpMatcher) Match(r *stdhttp.Request) bool {
	for _, re := range m {
		if re.MatchString(r.URL.Path) {
			return true
		}
	}
	return false
}
