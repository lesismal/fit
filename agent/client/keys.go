package client

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	stdhttp "net/http"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/lesismal/fit/agent/internal/mesh"
	"github.com/lesismal/fit/config"
)

// KeyPrefix starts every client API key: "ah-" + 32 random bytes,
// base64url, as agenthub made them, so that its keys go on working.
const KeyPrefix = "ah-"

// shownPrefix is how many leading characters of a key the pages keep
// showing.
const shownPrefix = 10

// KeyView is an API key as the pages list it. LastUsedAt and Calls are
// counted in memory, since the role started; they are not written back.
type KeyView struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Prefix     string `json:"prefix"`
	CreatedAt  int64  `json:"created_at"`
	LastUsedAt int64  `json:"last_used_at"`
	Calls      int64  `json:"calls"`
}

// KeyCreateReq / KeyCreateResp: MethodClientKeysCreate; Key is shown this
// once only.
type KeyCreateReq struct {
	Name string `json:"name"`
}

type KeyCreateResp struct {
	KeyView
	Key string `json:"key"`
}

// KeyIDReq names a key.
type KeyIDReq struct {
	ID string `json:"id"`
}

type usage struct {
	last  int64
	calls int64
}

// keyStore holds the API keys of the config, and their use.
type keyStore struct {
	mu    sync.Mutex
	keys  []config.AgentClientKey
	usage map[string]*usage // by key ID, only for keys of the config
}

func hashKey(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:])
}

// set replaces the keys with those of the config; the use of the keys kept
// stays.
func (s *keyStore) set(keys []config.AgentClientKey) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.keys = keys
	u := make(map[string]*usage, len(keys))
	for _, k := range keys {
		if old := s.usage[k.ID]; old != nil {
			u[k.ID] = old
		} else {
			u[k.ID] = &usage{}
		}
	}
	s.usage = u
}

func (s *keyStore) list() []KeyView {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]KeyView, 0, len(s.keys))
	for _, k := range s.keys {
		v := KeyView{ID: k.ID, Name: k.Name, Prefix: k.Prefix, CreatedAt: k.CreatedAt}
		if u := s.usage[k.ID]; u != nil {
			v.LastUsedAt, v.Calls = u.last, u.calls
		}
		out = append(out, v)
	}
	return out
}

// newKey makes a key: the secret, shown once, and what the config keeps.
func newKey(name string) (config.AgentClientKey, string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return config.AgentClientKey{}, "", mesh.BadRequest("name is required")
	}
	if utf8.RuneCountInString(name) > 64 {
		return config.AgentClientKey{}, "", mesh.BadRequest("name is too long (64 characters at most)")
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return config.AgentClientKey{}, "", err
	}
	secret := KeyPrefix + base64.RawURLEncoding.EncodeToString(b)
	id := make([]byte, 8)
	_, _ = rand.Read(id)
	return config.AgentClientKey{ID: hex.EncodeToString(id), Name: name, Prefix: secret[:shownPrefix], Hash: hashKey(secret),
		CreatedAt: time.Now().Unix()}, secret, nil
}

// auth checks the key of an agent request (Authorization: Bearer, or
// x-api-key) against every hash kept, in constant time, and counts the use.
func (s *keyStore) auth(h stdhttp.Header) bool {
	key := requestKey(h)
	if key == "" {
		return false
	}
	sum := []byte(hashKey(key))
	s.mu.Lock()
	defer s.mu.Unlock()
	found := -1
	for i := range s.keys {
		if subtle.ConstantTimeCompare(sum, []byte(s.keys[i].Hash)) == 1 {
			found = i
		}
	}
	if found < 0 {
		return false
	}
	if u := s.usage[s.keys[found].ID]; u != nil {
		u.last = time.Now().Unix()
		u.calls++
	}
	return true
}

// requestKey returns the API key of an agent request.
func requestKey(h stdhttp.Header) string {
	if a := strings.TrimSpace(h.Get("Authorization")); len(a) > 7 && strings.EqualFold(a[:7], "bearer ") {
		return strings.TrimSpace(a[7:])
	}
	return strings.TrimSpace(h.Get("x-api-key"))
}
