// Package managed is what a manager of many fits (package cluster) tells a
// member it owns in the member's config, and how both sides tell whether
// the member still holds what the manager last saw there: the member keeps
// the record (admin's /node/managed) and marks those parts of its config on
// its own pages, and the manager compares each part with what its templates
// render. See docs/decisions/033-cluster-management.md, I145.
//
// A part is a top-level member of the config whole (log, media_auth, ...),
// or an element of one of the named lists (Lists), by its name. A part's
// value is compared by its Hash, the SHA-256 of its canonical JSON, so that
// the order of an object's members and the spaces between them do not
// count.
package managed

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"time"
	"unicode/utf8"
)

// Section is the store's section a member keeps its record in.
const Section = "managed"

// The bounds of a record.
const (
	// MaxPaths bounds the parts a record lists.
	MaxPaths = 4096
	// MaxNameBytes bounds an element's name, a template's and the
	// manager's.
	MaxNameBytes = 256
	// MaxRecordBytes bounds a record as JSON.
	MaxRecordBytes = 1 << 20
)

// Lists are the config's top-level lists whose elements a manager owns one
// by one, by name; any other top-level member is owned whole.
var Lists = []string{"listeners", "routes", "services", "upstreams"}

// IsList reports whether the top-level member is one of Lists.
func IsList(member string) bool { return slices.Contains(Lists, member) }

// Path is one part of a member's config a manager owns: the top-level
// member Member whole, when Name is empty, or the element of the list
// Member whose name is Name. Template names the manager's template the part
// comes from. Hash is the part's value as the manager last saw it on the
// member in sync with the template, "" when it has not seen it so.
type Path struct {
	Member   string `json:"member"`
	Name     string `json:"name,omitempty"`
	Template string `json:"template,omitempty"`
	Hash     string `json:"hash,omitempty"`
}

// String writes p as the pages show it: log, or routes[api].
func (p Path) String() string { return Display(p.Member, p.Name) }

// Display writes a part as the pages show it.
func Display(member, name string) string {
	if name == "" {
		return member
	}
	return member + "[" + name + "]"
}

// Same reports whether p and q are the same part.
func (p Path) Same(q Path) bool { return p.Member == q.Member && p.Name == q.Name }

// Record is what a manager told a member it owns: who the manager is, when
// it said so, and the parts.
type Record struct {
	// Manager is the manager's node ID, and ManagerHost its host's name.
	Manager     string    `json:"manager"`
	ManagerHost string    `json:"manager_host,omitempty"`
	Updated     time.Time `json:"updated,omitzero"`
	Paths       []Path    `json:"paths"`
}

var (
	memberPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	hashPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Unownable are the top-level members no manager owns: what the config
// API refuses to change, and the config's own version.
var Unownable = []string{"admin", "schema_version"}

// CheckPart checks a part's member and name: a top-level member's name, not
// one of Unownable, and a name exactly when the member is one of Lists.
func CheckPart(member, name string) error {
	switch {
	case !memberPattern.MatchString(member) || len(member) > 64:
		return fmt.Errorf("%q is not a top-level member of the config", member)
	case slices.Contains(Unownable, member):
		return fmt.Errorf("%s is the member's own: no manager owns it", member)
	case IsList(member) && name == "":
		return fmt.Errorf("an element of %s is owned by its name, which it has none of", member)
	case !IsList(member) && name != "":
		return fmt.Errorf("%s is owned whole, not by element", member)
	case len(name) > MaxNameBytes || !utf8.ValidString(name):
		return fmt.Errorf("the name of an element of %s is at most %d bytes of UTF-8", member, MaxNameBytes)
	}
	return nil
}

// Check checks r as a member keeps it.
func (r *Record) Check() error {
	switch {
	case len(r.Manager) > MaxNameBytes || !utf8.ValidString(r.Manager):
		return fmt.Errorf("manager: at most %d bytes of UTF-8", MaxNameBytes)
	case len(r.ManagerHost) > MaxNameBytes || !utf8.ValidString(r.ManagerHost):
		return fmt.Errorf("manager_host: at most %d bytes of UTF-8", MaxNameBytes)
	case len(r.Paths) > MaxPaths:
		return fmt.Errorf("paths: %d parts, more than %d", len(r.Paths), MaxPaths)
	}
	for i, p := range r.Paths {
		if err := CheckPart(p.Member, p.Name); err != nil {
			return fmt.Errorf("paths[%d]: %v", i, err)
		}
		if len(p.Template) > MaxNameBytes || !utf8.ValidString(p.Template) {
			return fmt.Errorf("paths[%d].template: at most %d bytes of UTF-8", i, MaxNameBytes)
		}
		if p.Hash != "" && !hashPattern.MatchString(p.Hash) {
			return fmt.Errorf("paths[%d].hash: 64 lower-case hex digits, or none", i)
		}
		for _, q := range r.Paths[:i] {
			if p.Same(q) {
				return fmt.Errorf("paths[%d]: %s is listed twice", i, p)
			}
		}
	}
	return nil
}

// Normalize sorts r's parts, so that two records listing the same parts
// are equal.
func (r *Record) Normalize() {
	if r.Paths == nil {
		r.Paths = []Path{}
	}
	sort.Slice(r.Paths, func(i, j int) bool {
		a, b := r.Paths[i], r.Paths[j]
		if a.Member != b.Member {
			return a.Member < b.Member
		}
		return a.Name < b.Name
	})
}

// Equal reports whether r and o say the same, but for when: the same
// manager and parts, each from the same template with the same hash.
func (r *Record) Equal(o *Record) bool {
	return r.Manager == o.Manager && r.ManagerHost == o.ManagerHost && slices.Equal(r.Paths, o.Paths)
}

// ETag is the record's entity tag: a hash of what it says, its time left
// out; an empty record's is that of no parts.
func (r *Record) ETag() string {
	c := *r
	c.Updated = time.Time{}
	c.Normalize()
	data, _ := json.Marshal(c) // strings
	sum := sha256.Sum256(data)
	return strconv.Quote("m" + hex.EncodeToString(sum[:8]))
}

// Canonical returns the JSON value data with its objects' members sorted
// by name and no spaces: two values that mean the same are the same bytes.
// Numbers are kept as written.
func Canonical(data []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("more than one JSON value")
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimRight(b.Bytes(), "\n"), nil
}

// Hash returns the SHA-256 of data's canonical JSON, in hex; "" when data is
// not JSON.
func Hash(data []byte) string {
	c, err := Canonical(data)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(c)
	return hex.EncodeToString(sum[:])
}

// Lookup returns the part member, name of the config cfg, a JSON object:
// the top-level member whole, or the element of the list member whose name
// is name; false when the config has no such part.
func Lookup(cfg []byte, member, name string) (json.RawMessage, bool, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(cfg, &top); err != nil {
		return nil, false, fmt.Errorf("the config is not a JSON object: %v", err)
	}
	return LookupIn(top, member, name)
}

// LookupIn is Lookup on a config's top-level members.
func LookupIn(top map[string]json.RawMessage, member, name string) (json.RawMessage, bool, error) {
	v, ok := top[member]
	if !ok {
		return nil, false, nil
	}
	if name == "" {
		return v, true, nil
	}
	var items []json.RawMessage
	if err := json.Unmarshal(v, &items); err != nil {
		return nil, false, fmt.Errorf("%s is not a list: %v", member, err)
	}
	for _, it := range items {
		if NameOf(it) == name {
			return it, true, nil
		}
	}
	return nil, false, nil
}

// NameOf returns the name of a list's element, an object's "name" when it
// is a string; "" otherwise.
func NameOf(item json.RawMessage) string {
	var n struct {
		Name json.RawMessage `json:"name"`
	}
	if json.Unmarshal(item, &n) != nil {
		return ""
	}
	var s string
	if json.Unmarshal(n.Name, &s) != nil {
		return ""
	}
	return s
}
