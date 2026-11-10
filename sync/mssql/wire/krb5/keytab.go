package krb5

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Keytab is a keytab file's keys (MIT's format, version 0x502).
type Keytab struct {
	Entries []KeytabEntry
}

// KeytabEntry is one key of a principal.
type KeytabEntry struct {
	Realm string
	Parts []string
	KVNO  uint32
	Key   Key
}

// LoadKeytab reads a keytab file.
func LoadKeytab(path string) (*Keytab, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	kt, err := ParseKeytab(b)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return kt, nil
}

var errKeytab = errors.New("krb5: a malformed keytab")

// ParseKeytab reads a keytab.
func ParseKeytab(b []byte) (*Keytab, error) {
	if len(b) < 2 || b[0] != 5 {
		return nil, errKeytab
	}
	if b[1] != 2 {
		return nil, fmt.Errorf("krb5: keytab version 0x5%02x; only 0x502 is read", b[1])
	}
	kt := &Keytab{}
	b = b[2:]
	for len(b) >= 4 {
		size := int32(binary.BigEndian.Uint32(b))
		b = b[4:]
		if size < 0 {
			// A hole, a deleted entry.
			if int(-size) > len(b) {
				return nil, errKeytab
			}
			b = b[-size:]
			continue
		}
		if int(size) > len(b) {
			return nil, errKeytab
		}
		e, err := parseEntry(b[:size])
		if err != nil {
			return nil, err
		}
		kt.Entries = append(kt.Entries, e)
		b = b[size:]
	}
	return kt, nil
}

func parseEntry(b []byte) (KeytabEntry, error) {
	var e KeytabEntry
	u16 := func() (int, bool) {
		if len(b) < 2 {
			return 0, false
		}
		v := int(binary.BigEndian.Uint16(b))
		b = b[2:]
		return v, true
	}
	str := func() (string, bool) {
		n, ok := u16()
		if !ok || n > len(b) {
			return "", false
		}
		s := string(b[:n])
		b = b[n:]
		return s, true
	}
	n, ok := u16()
	if !ok {
		return e, errKeytab
	}
	if e.Realm, ok = str(); !ok {
		return e, errKeytab
	}
	for i := 0; i < n; i++ {
		p, ok := str()
		if !ok {
			return e, errKeytab
		}
		e.Parts = append(e.Parts, p)
	}
	// The name type and the timestamp, then the 8-bit version.
	if len(b) < 9 {
		return e, errKeytab
	}
	e.KVNO = uint32(b[8])
	b = b[9:]
	et, ok1 := u16()
	kl, ok2 := u16()
	if !ok1 || !ok2 || kl > len(b) {
		return e, errKeytab
	}
	e.Key = Key{EType: int32(et), Value: append([]byte(nil), b[:kl]...)}
	b = b[kl:]
	// The 32-bit version, when there is one, replaces the 8-bit one.
	if len(b) >= 4 {
		if v := binary.BigEndian.Uint32(b); v != 0 {
			e.KVNO = v
		}
	}
	return e, nil
}

// Key returns the key of a principal and encryption type of the highest
// version.
func (kt *Keytab) Key(realm string, parts []string, etype int32) (Key, bool) {
	var best *KeytabEntry
	for i := range kt.Entries {
		e := &kt.Entries[i]
		if e.Realm != realm || e.Key.EType != etype || strings.Join(e.Parts, "/") != strings.Join(parts, "/") {
			continue
		}
		if best == nil || e.KVNO > best.KVNO {
			best = e
		}
	}
	if best == nil {
		return Key{}, false
	}
	return best.Key, true
}

// Config is what a krb5.conf says of realms: the default one and each
// one's KDCs.
type Config struct {
	DefaultRealm string
	KDCs         map[string][]string
}

// LoadConfig reads a krb5.conf: libdefaults' default_realm and realms'
// kdc lines; the rest is not read.
func LoadConfig(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ParseConfig(f)
}

// ParseConfig reads a krb5.conf's text.
func ParseConfig(r io.Reader) (*Config, error) {
	c := &Config{KDCs: map[string][]string{}}
	section, realm := "", ""
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if i := strings.IndexAny(line, "#;"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		switch {
		case line == "":
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			section, realm = strings.TrimSpace(line[1:len(line)-1]), ""
		case line == "}":
			realm = ""
		default:
			k, v, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			k, v = strings.TrimSpace(k), strings.TrimSpace(v)
			switch {
			case section == "libdefaults" && k == "default_realm":
				c.DefaultRealm = v
			case section == "realms" && v == "{":
				realm = k
			case section == "realms" && realm != "" && k == "kdc":
				c.KDCs[realm] = append(c.KDCs[realm], v)
			}
		}
	}
	return c, sc.Err()
}
