// Package store keeps a config encrypted in a file of fit's own format,
// fitstore: a header that says how the key is derived from a password, then
// the config sealed with XChaCha20-Poly1305, the header its additional data.
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
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

// The header's layout.
const (
	magic      = "FITSTORE"
	version    = 1
	kdfArgon2  = 1
	saltLen    = 16
	headerLen  = len(magic) + 1 + 1 + 4 + 4 + 1 + saltLen + chacha20poly1305.NonceSizeX
	saltOffset = headerLen - chacha20poly1305.NonceSizeX - saltLen
)

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
	mu       sync.Mutex // held by Read and Write, which may come from two goroutines
	path     string
	password string // for a file another store wrote, with a salt of its own
	params   Params
	salt     [saltLen]byte
	aead     interface {
		Seal(dst, nonce, plaintext, additionalData []byte) []byte
		Open(dst, nonce, ciphertext, additionalData []byte) ([]byte, error)
	}
}

// Open opens the store at path with password and returns what it holds, or
// nil data when there is no file there yet: then the store has a new salt,
// and its first Write makes the file.
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
// it, and returns what it holds; the key is derived again only when the
// file's salt or settings are not the store's.
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
// one s has.
func (s *Store) open(file []byte) ([]byte, error) {
	params, salt, err := parseHeader(s.path, file)
	if err != nil {
		return nil, err
	}
	if s.aead == nil || params != s.params || salt != s.salt {
		s.params, s.salt = params, salt
		s.derive()
	}
	data, err := s.aead.Open(nil, file[saltOffset+saltLen:headerLen], file[headerLen:], file[:headerLen])
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrOpen, s.path)
	}
	return data, nil
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

func parseHeader(path string, file []byte) (p Params, salt [saltLen]byte, err error) {
	if len(file) < headerLen+chacha20poly1305.Overhead || string(file[:len(magic)]) != magic {
		return p, salt, fmt.Errorf("%w: %s", ErrNotStore, path)
	}
	h := file[len(magic):]
	if h[0] != version {
		return p, salt, fmt.Errorf("store: %s is fitstore version %d; this fit reads version %d", path, h[0], version)
	}
	if h[1] != kdfArgon2 {
		return p, salt, fmt.Errorf("store: %s derives its key by method %d, which this fit does not know", path, h[1])
	}
	p = Params{Time: binary.BigEndian.Uint32(h[2:]), MemoryKiB: binary.BigEndian.Uint32(h[6:]), Threads: h[10]}
	if p.Time == 0 || p.Time > maxTime || p.Threads == 0 || p.MemoryKiB < 8*uint32(p.Threads) || p.MemoryKiB > maxMemoryKiB {
		return p, salt, fmt.Errorf("store: %s asks for Argon2id time %d, %d KiB and %d threads, past what fit takes", path, p.Time, p.MemoryKiB, p.Threads)
	}
	copy(salt[:], file[saltOffset:])
	return p, salt, nil
}

func (s *Store) derive() {
	key := argon2.IDKey([]byte(s.password), s.salt[:], s.params.Time, s.params.MemoryKiB, s.params.Threads, chacha20poly1305.KeySize)
	s.aead, _ = chacha20poly1305.NewX(key) // the key is the right size
	clear(key)
}

// Path returns the store's file.
func (s *Store) Path() string { return s.path }

// Write seals data with a new nonce and replaces the store's file with it:
// written to a temporary file beside it, made 0600, synced and renamed over
// it, so that the store is either the old one or the new one.
func (s *Store) Write(data []byte) error {
	if len(data) > MaxBytes {
		return fmt.Errorf("store: the config is larger than %d bytes", MaxBytes)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.seal(data)
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
	return nil
}

// seal returns the store's file for data: the header, then data sealed.
func (s *Store) seal(data []byte) []byte {
	var h bytes.Buffer
	h.Grow(headerLen + len(data) + chacha20poly1305.Overhead)
	h.WriteString(magic)
	h.WriteByte(version)
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
