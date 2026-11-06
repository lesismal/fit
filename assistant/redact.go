package assistant

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strings"
)

// Redacted is what a secret is replaced with.
const Redacted = "[redacted]"

// Out of reach of every tool: the members of the config the assistant never
// hands a model, whatever is redacted in them. admin.assistant is the
// assistant's own settings, admin.console.users the console's passwords,
// admin.store the store and its key's source, and admin.address and
// admin.token_file the Admin API's own address and token.
var hiddenMembers = []string{"admin.assistant", "admin.console.users", "admin.store", "admin.address", "admin.token_file"}

// secretPatterns find secrets in text: private keys, password hashes,
// tokens and the keys of well-known APIs, and secrets written as name=value.
var secretPatterns = []struct {
	re   *regexp.Regexp
	with string
}{
	{regexp.MustCompile(`-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----(?s:.*?)(-----END [A-Z0-9 ]*PRIVATE KEY-----|$)`), "[redacted private key]"},
	{regexp.MustCompile(`\$2[abxy]?\$\d{2}\$[./A-Za-z0-9]{53}`), Redacted},
	{regexp.MustCompile(`\$argon2(?:id|i|d)\$[^\s"'\\]+`), Redacted},
	{regexp.MustCompile(`\$[56]\$[^\s"'\\:]+`), Redacted},
	{regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`), Redacted},
	{regexp.MustCompile(`(?i)\b(bearer|basic)(\s+)[A-Za-z0-9._~+/=-]{8,}`), "${1}${2}" + Redacted},
	{regexp.MustCompile(`\b(?:sk|pk|rk)-[A-Za-z0-9_-]{16,}`), Redacted},
	{regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`), Redacted},
	{regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}`), Redacted},
	{regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}`), Redacted},
	{regexp.MustCompile(`\bAIza[0-9A-Za-z_-]{30,}`), Redacted},
	{regexp.MustCompile(`(?i)\b((?:[a-z]+[_-])?(?:password|passwd|secret|token|api[_-]?key|access[_-]?key|authorization))(\s*[=:]\s*)("[^"]*"|'[^']*'|[^\s,;&"']+)`), "${1}${2}" + Redacted},
}

// redactor replaces the secrets of what a tool hands the model: the
// providers' keys it knows, the patterns above and the settings' own.
type redactor struct {
	literals []string
	extra    []*regexp.Regexp
}

// newRedactor returns a redactor of the keys literals, and of the patterns
// extra (data.redact, checked when the settings loaded).
func newRedactor(literals []string, extra []string) *redactor {
	r := &redactor{}
	for _, l := range literals {
		if len(l) >= 8 {
			r.literals = append(r.literals, l)
		}
	}
	for _, e := range extra {
		if re, err := regexp.Compile(e); err == nil {
			r.extra = append(r.extra, re)
		}
	}
	return r
}

// text returns s with its secrets replaced.
func (r *redactor) text(s string) string {
	for _, l := range r.literals {
		s = strings.ReplaceAll(s, l, Redacted)
	}
	for _, p := range secretPatterns {
		s = p.re.ReplaceAllString(s, p.with)
	}
	for _, re := range r.extra {
		s = re.ReplaceAllString(s, Redacted)
	}
	return s
}

// secretName reports whether a member named name holds a secret: a
// password, a key, a token or a hash, but not a file or a variable naming
// one, nor a limit.
func secretName(name string) bool {
	n := strings.ToLower(name)
	switch {
	case strings.HasSuffix(n, "_file") || strings.HasSuffix(n, "_env") || strings.HasSuffix(n, "_ttl") ||
		strings.HasPrefix(n, "max_") || strings.HasPrefix(n, "min_"):
		return false
	case strings.Contains(n, "password") || strings.Contains(n, "secret") || strings.Contains(n, "passphrase"):
		return true
	}
	switch n {
	case "passwd", "pass", "token", "tokens", "key", "keys", "apikey", "hash", "credential", "credentials",
		"authorization", "cookie", "signature", "psk", "private_key":
		return true
	}
	for _, suffix := range []string{"_token", "_tokens", "_key", "_keys", "_hash", "_sha256", "_signature"} {
		if strings.HasSuffix(n, suffix) {
			return true
		}
	}
	return false
}

// errNotJSON is a tool's output that should have been JSON.
var errNotJSON = errors.New("not JSON")

// redactJSON returns data, JSON, compacted, with the members hide names
// (dotted paths from data's root) left out, every string under a member
// secretName names replaced, and the text of what is left redacted.
func (r *redactor) redactJSON(data []byte, hide []string) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var b bytes.Buffer
	w := jsonRewriter{dec: dec, b: &b, hide: hide}
	if err := w.value("", false); err != nil {
		return nil, errNotJSON
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errNotJSON
	}
	return []byte(r.text(b.String())), nil
}

// jsonRewriter copies JSON from dec to b a token at a time, keeping the
// members' order.
type jsonRewriter struct {
	dec  *json.Decoder
	b    *bytes.Buffer
	hide []string
}

// value copies the value at path; under a secret member, its strings are
// replaced.
func (w *jsonRewriter) value(path string, secret bool) error {
	tok, err := w.dec.Token()
	if err != nil {
		return err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			w.b.WriteByte('{')
			first := true
			for w.dec.More() {
				k, err := w.dec.Token()
				if err != nil {
					return err
				}
				key, _ := k.(string)
				sub := key
				if path != "" {
					sub = path + "." + key
				}
				if contains(w.hide, sub) {
					if err := w.skip(); err != nil {
						return err
					}
					continue
				}
				if !first {
					w.b.WriteByte(',')
				}
				first = false
				kb, _ := json.Marshal(key)
				w.b.Write(kb)
				w.b.WriteByte(':')
				if err := w.value(sub, secret || secretName(key)); err != nil {
					return err
				}
			}
			w.b.WriteByte('}')
		case '[':
			w.b.WriteByte('[')
			for i := 0; w.dec.More(); i++ {
				if i > 0 {
					w.b.WriteByte(',')
				}
				if err := w.value(path, secret); err != nil {
					return err
				}
			}
			w.b.WriteByte(']')
		}
		_, err := w.dec.Token() // the closing delimiter
		return err
	case string:
		if secret && t != "" {
			t = Redacted
		}
		sb, _ := json.Marshal(t)
		w.b.Write(sb)
	case json.Number:
		w.b.WriteString(t.String())
	case bool:
		if t {
			w.b.WriteString("true")
		} else {
			w.b.WriteString("false")
		}
	default: // nil
		w.b.WriteString("null")
	}
	return nil
}

// skip consumes a value.
func (w *jsonRewriter) skip() error {
	var v json.RawMessage
	return w.dec.Decode(&v)
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
