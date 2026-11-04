// Package store keeps a config encrypted in a file of fit's own format,
// fitstore: a header that says how the key is derived from a password, then
// the config sealed with XChaCha20-Poly1305, the header its additional data.
// Beside the config, the store keeps the console users whose passwords
// changed since fit started, so that a changed password survives a restart.
// See docs/decisions/027-console-and-store.md.
//
// Opening and writing a store read and write files, and deriving its key
// takes 64 MiB and a fair share of a second: both are for the control plane,
// never a fib worker.
package store

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

// The header's layout.
const (
	magic = "FITSTORE"
	// version is the format this fit writes: the sealed payload is an
	// envelope of the base config and the console users kept beside it.
	version = 2
	// versionConfig is the first format, whose sealed payload is the base
	// config alone; it is still read.
	versionConfig = 1
	kdfArgon2     = 1
	saltLen       = 16
	headerLen     = len(magic) + 1 + 1 + 4 + 4 + 1 + saltLen + chacha20poly1305.NonceSizeX
	saltOffset    = headerLen - chacha20poly1305.NonceSizeX - saltLen
)

// User is a console user kept beside the config in the store: the name, and
// the bcrypt hash of the password that replaced the one the bootstrap file
// gives, so that fit restarted logs in with the changed password.
type User struct {
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash"`
}

// payload is the plaintext of a version 2 store: the base config, and the
// users whose password hashes replace those of the config's admin.
type payload struct {
	Config json.RawMessage `json:"config"`
	Users  []User          `json:"users,omitempty"`
}

// Params are Argon2id's settings.
type Params struct {
	Time      uint32
	MemoryKiB uint32
	Threads   uint8
}

// DefaultParams are those a new store is written with: RFC 9106's second
// recommended setting. A variable for tests to make cheaper.
var DefaultParams = Params{Time: 3, MemoryKiB: 64 << 10, Threads: 4}

// The bounds on what a store's header may ask for, so that a file made to
// be expensive cannot make fit spend more than this deriving its key.
const (
	maxTime      = 16
	maxMemoryKiB = 1 << 20 // 1 GiB
)

// MaxBytes is the largest store opened.
const MaxBytes = 64 << 20

// Errors opening a store.
var (
	ErrNotStore = errors.New("store: not a fitstore file")
	// ErrOpen is a store that does not open: the password is wrong, or the
	// file was changed.
	ErrOpen = errors.New("store: the store does not open with the password: it is wrong, or the file was changed")
)

// Store is an open store: its path, and the key its password gives with its
// salt, kept so that writes need no derivation.
type Store struct {
	mu       sync.Mutex // held by Read, Write and SetUsers, which may come from two goroutines
	path     string
	password string // for a file another store wrote, with a salt of its own
	params   Params
	salt     [saltLen]byte
	aead     interface {
		Seal(dst, nonce, plaintext, additionalData []byte) []byte
		Open(dst, nonce, ciphertext, additionalData []byte) ([]byte, error)
	}
	// data is the base config last read or written, and users the console
	// users kept beside it; SetUsers seals data again with new users, and
	// Write keeps the old ones.
	data  []byte
	users []User
}

// Open opens the store at path with password and returns the base config it
// holds, the console users remembered in the store, or nil data when there
// is no file there yet: then the store has a new salt, and its first Write
// makes the file.
func Open(path, password string) (*Store, []byte, error) {
	if password == "" {
		return nil, nil, errors.New("store: the password is empty")
	}
	file, err := readFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		s, err := Create(path, password)
		return s, nil, err
	}
	if err != nil {
		return nil, nil, err
	}
	s := &Store{path: path, password: password}
	data, err := s.open(file)
	if err != nil {
		return nil, nil, err
	}
	return s, data, nil
}

// Create returns a store at path with a new salt, whatever is there: its
// first Write replaces it.
func Create(path, password string) (*Store, error) {
	if password == "" {
		return nil, errors.New("store: the password is empty")
	}
	s := &Store{path: path, password: password, params: DefaultParams}
	if _, err := rand.Read(s.salt[:]); err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	s.derive()
	return s, nil
}

// Read reads the store's file again, as another process may have written
// it, and returns the base config it holds, the console users remembered in
// the store; the key is derived again only when the file's salt or settings
// are not the store's.
func (s *Store) Read() ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	file, err := readFile(s.path)
	if err != nil {
		return nil, err
	}
	return s.open(file)
}

// open opens file, a store's whole file, deriving its key unless it is the
// one s has, and returns what it holds: the base config, the users being
// remembered in s.
func (s *Store) open(file []byte) ([]byte, error) {
	h, err := parseHeader(s.path, file)
	if err != nil {
		return nil, err
	}
	if s.aead == nil || h.params != s.params || h.salt != s.salt {
		s.params, s.salt = h.params, h.salt
		s.derive()
	}
	plaintext, err := s.aead.Open(nil, file[saltOffset+saltLen:headerLen], file[headerLen:], file[:headerLen])
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrOpen, s.path)
	}
	data, users, err := s.decode(h.version, plaintext)
	if err != nil {
		return nil, err
	}
	s.data, s.users = data, users
	return data, nil
}

// decode returns the base config and the users a store of version ver
// holds, plaintext being what its key seals.
func (s *Store) decode(ver byte, plaintext []byte) ([]byte, []User, error) {
	if ver == versionConfig {
		return plaintext, nil, nil
	}
	var p payload
	if err := json.Unmarshal(plaintext, &p); err != nil || len(p.Config) == 0 {
		return nil, nil, fmt.Errorf("store: %s holds no config", s.path)
	}
	return p.Config, p.Users, nil
}

// encode returns the payload of a version 2 store: the config as it was
// written, its layout kept, and the users beside it. The config is JSON,
// since the payload is: one that is not is refused before anything is
// written, rather than making a store nothing can open.
func encode(data []byte, users []User) ([]byte, error) {
	if !json.Valid(data) {
		return nil, errors.New("store: the config is not JSON")
	}
	var b bytes.Buffer
	b.Grow(len(data) + 32 + len(users)*128)
	b.WriteString(`{"config":`)
	b.Write(data)
	if len(users) > 0 {
		u, err := json.Marshal(users)
		if err != nil {
			return nil, fmt.Errorf("store: the users: %w", err)
		}
		b.WriteString(`,"users":`)
		b.Write(u)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

func readFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, int64(MaxBytes+headerLen+chacha20poly1305.Overhead+1)))
	if err != nil {
		return nil, fmt.Errorf("store: %w", err)
	}
	if len(data) > MaxBytes+headerLen+chacha20poly1305.Overhead {
		return nil, fmt.Errorf("store: %s is larger than %d bytes", path, MaxBytes)
	}
	return data, nil
}

// header is what a store's header says.
type header struct {
	version byte
	params  Params
	salt    [saltLen]byte
}

func parseHeader(path string, file []byte) (h header, err error) {
	if len(file) < headerLen+chacha20poly1305.Overhead || string(file[:len(magic)]) != magic {
		return h, fmt.Errorf("%w: %s", ErrNotStore, path)
	}
	b := file[len(magic):]
	if b[0] != version && b[0] != versionConfig {
		return h, fmt.Errorf("store: %s is fitstore version %d; this fit reads versions %d and %d", path, b[0], versionConfig, version)
	}
	if b[1] != kdfArgon2 {
		return h, fmt.Errorf("store: %s derives its key by method %d, which this fit does not know", path, b[1])
	}
	h.version = b[0]
	h.params = Params{Time: binary.BigEndian.Uint32(b[2:]), MemoryKiB: binary.BigEndian.Uint32(b[6:]), Threads: b[10]}
	if h.params.Time == 0 || h.params.Time > maxTime || h.params.Threads == 0 || h.params.MemoryKiB < 8*uint32(h.params.Threads) || h.params.MemoryKiB > maxMemoryKiB {
		return h, fmt.Errorf("store: %s asks for Argon2id time %d, %d KiB and %d threads, past what fit takes", path, h.params.Time, h.params.MemoryKiB, h.params.Threads)
	}
	copy(h.salt[:], file[saltOffset:])
	return h, nil
}

func (s *Store) derive() {
	key := argon2.IDKey([]byte(s.password), s.salt[:], s.params.Time, s.params.MemoryKiB, s.params.Threads, chacha20poly1305.KeySize)
	s.aead, _ = chacha20poly1305.NewX(key) // the key is the right size
	clear(key)
}

// Path returns the store's file.
func (s *Store) Path() string { return s.path }

// Seal returns a fitstore file holding data, a base config as JSON, and
// users, sealed with the store's own key, as an export of it that opens with
// the store's password; the store's file is not touched.
func (s *Store) Seal(data []byte, users []User) ([]byte, error) {
	plaintext, err := encode(data, users)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.seal(version, plaintext), nil
}

// Unseal opens file, a whole fitstore file such as an export, with the
// store's password, and returns the base config and the users it holds; the
// store's file and what the store remembers are not touched.
func (s *Store) Unseal(file []byte) ([]byte, []User, error) {
	return Unseal(file, s.password)
}

// Seal returns a fitstore file holding data and users, sealed with a key
// password gives with a new salt, as Create's first Write would make it.
func Seal(password string, data []byte, users []User) ([]byte, error) {
	s, err := Create("", password)
	if err != nil {
		return nil, err
	}
	return s.Seal(data, users)
}

// Unseal opens file, a whole fitstore file, with password, and returns the
// base config and the users it holds.
func Unseal(file []byte, password string) ([]byte, []User, error) {
	if password == "" {
		return nil, nil, errors.New("store: the password is empty")
	}
	if len(file) > MaxBytes+headerLen+chacha20poly1305.Overhead {
		return nil, nil, fmt.Errorf("store: the file is larger than %d bytes", MaxBytes)
	}
	s := &Store{path: "the file", password: password}
	data, err := s.open(file)
	if err != nil {
		return nil, nil, err
	}
	return data, s.users, nil
}

// Users returns the console users kept beside the config.
func (s *Store) Users() []User {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.users)
}

// SetUsers replaces the console users kept beside the config with users, and
// remembers them, so that they are written with every config Write seals
// from here on. A store whose config has not been read or written yet keeps
// no users, and gives an error.
func (s *Store) SetUsers(users []User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data == nil {
		return errors.New("store: the store holds no config yet, so it can keep no users")
	}
	return s.write(s.data, slices.Clone(users))
}

// Write seals data with a new nonce and replaces the store's file with it,
// beside the users the store keeps: written to a temporary file beside it,
// made 0600, synced and renamed over it, so that the store is either the old
// one or the new one. Write keeps data, which the caller must not change
// afterwards, since SetUsers seals it again.
func (s *Store) Write(data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.write(data, s.users)
}

// write seals data and users, the store's payload, and replaces the store's
// file with it. s.mu must be held.
func (s *Store) write(data []byte, users []User) error {
	if len(data) > MaxBytes {
		return fmt.Errorf("store: the config is larger than %d bytes", MaxBytes)
	}
	plaintext, err := encode(data, users)
	if err != nil {
		return err
	}
	if len(plaintext) > MaxBytes {
		return fmt.Errorf("store: the config and its users are larger than %d bytes", MaxBytes)
	}
	out := s.seal(version, plaintext)
	dir := filepath.Dir(s.path)
	f, err := os.CreateTemp(dir, "."+filepath.Base(s.path)+".*")
	if err != nil {
		return fmt.Errorf("store: %w", err)
	}
	tmp := f.Name()
	ok := false
	defer func() {
		if !ok {
			_ = f.Close()
			_ = os.Remove(tmp)
		}
	}()
	if err := f.Chmod(0o600); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if _, err := f.Write(out); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("store: %w", err)
	}
	ok = true
	syncDir(dir)
	s.data, s.users = data, users
	return nil
}

// seal returns the store's file for data, its plaintext, as a file of
// version ver: the header, then data sealed.
func (s *Store) seal(ver byte, data []byte) []byte {
	var h bytes.Buffer
	h.Grow(headerLen + len(data) + chacha20poly1305.Overhead)
	h.WriteString(magic)
	h.WriteByte(ver)
	h.WriteByte(kdfArgon2)
	h.Write(binary.BigEndian.AppendUint32(nil, s.params.Time))
	h.Write(binary.BigEndian.AppendUint32(nil, s.params.MemoryKiB))
	h.WriteByte(s.params.Threads)
	h.Write(s.salt[:])
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	_, _ = rand.Read(nonce) // crypto/rand.Read does not fail
	h.Write(nonce)
	header := h.Bytes()
	// The additional data is a copy: Seal's dst and additional data may
	// not overlap.
	return s.aead.Seal(header, nonce, data, bytes.Clone(header))
}
