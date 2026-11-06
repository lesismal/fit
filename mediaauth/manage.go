package mediaauth

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/lesismal/fib"

	"github.com/lesismal/fit/observe"
)

// What the Admin API and the workbench manage of a policy (I138): its keys
// rotated through their secret files, its live sessions counted, and its
// metrics.

// The errors of RotateFile, beside ErrUnavailable.
var (
	// ErrNoKey is a key the policy's keyring does not have.
	ErrNoKey = errors.New("mediaauth: no such key")
	// ErrInlineSecret is a secret written in the config, which no file
	// keeps: it is rotated by editing the config.
	ErrInlineSecret = errors.New("mediaauth: an inline secret is rotated by editing the config")
	// ErrKeyChoice is a rotation that names a key of a policy with a
	// single secret, or names neither a key nor all of a keyring.
	ErrKeyChoice = errors.New("mediaauth: the keys to rotate do not fit the policy")
)

// rotateMu orders RotateFile's rotations, so that a secret file and the
// running key end up the same whatever two rotations do at once.
var rotateMu sync.Mutex

// secretBytes is how many random bytes a new secret has.
const secretBytes = 32

// RotateFile rotates the policy's secrets through their files, as the
// handler media_tokens' revoke does: for the key of the keyring named key,
// for every key with all, or, for a policy with a single secret, neither
// given, the secret; each gets 32 new random bytes, in raw URL base64,
// written atomically to its secret_file, and then becomes the key tokens
// and URLs are signed and checked with, so that what the old one signed is
// refused at once while the keys left alone stay good. It returns the
// names of the keys rotated, in keyring order, or [""] for the single
// secret. Nothing is written when a key to rotate has no file
// (ErrInlineSecret), when key names no key (ErrNoKey), or when the keys
// named do not fit the policy (ErrKeyChoice). It writes files: call it off
// fib's workers.
func (p *Policy) RotateFile(key string, all bool) ([]string, error) {
	st := p.state()
	if st == nil {
		return nil, ErrUnavailable
	}
	type target struct{ key, file string }
	var targets []target
	switch ring := len(st.keys) > 0; {
	case !ring && (key != "" || all):
		return nil, fmt.Errorf("%w: the policy has one secret: name no key", ErrKeyChoice)
	case ring && key != "" && all:
		return nil, fmt.Errorf("%w: name a key or all, not both", ErrKeyChoice)
	case ring && key == "" && !all:
		return nil, fmt.Errorf("%w: name a key to rotate, or all", ErrKeyChoice)
	case !ring:
		file := p.SecretFile()
		if file == "" {
			return nil, ErrInlineSecret
		}
		targets = append(targets, target{file: file})
	default:
		for _, k := range st.keys {
			if !all && k.name != key {
				continue
			}
			if k.file == "" {
				return nil, fmt.Errorf("%w: key %q has no secret_file", ErrInlineSecret, k.name)
			}
			targets = append(targets, target{key: k.name, file: k.file})
		}
		if len(targets) == 0 {
			return nil, fmt.Errorf("%w: %q", ErrNoKey, key)
		}
	}
	rotateMu.Lock()
	defer rotateMu.Unlock()
	rotated := make([]string, 0, len(targets))
	for _, t := range targets {
		secret := newSecret()
		if err := writeSecret(t.file, secret); err != nil {
			return rotated, fmt.Errorf("mediaauth: writing the secret file: %w", err)
		}
		var err error
		if t.key == "" {
			err = p.Rotate(secret)
		} else {
			err = p.RotateKey(t.key, secret)
		}
		if err != nil {
			return rotated, err
		}
		rotated = append(rotated, t.key)
	}
	return rotated, nil
}

// newSecret returns a new signing secret: 32 random bytes, raw URL base64.
// crypto/rand's Read never returns an error.
func newSecret() string {
	var b [secretBytes]byte
	_, _ = rand.Read(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// writeSecret writes secret to path atomically, as media_tokens does: a
// temporary file in the same directory, mode 0600, renamed over path, so
// that a reader sees the old secret or the new one, never a part of one.
func writeSecret(path, secret string) error {
	f, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.WriteString(secret); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// LiveSessions returns how many of the policy's playback sessions are
// playing: not over, and not ended. It copies nothing.
func (p *Policy) LiveSessions() int {
	var idle time.Duration
	if st := p.state(); st != nil && st.url != nil {
		idle = st.url.idle
	}
	n := now().UnixNano()
	live := 0
	for i := range p.sess.shards {
		sh := &p.sess.shards[i]
		sh.mu.Lock()
		for _, l := range sh.subjects {
			for _, s := range l.list {
				if !s.ended && !s.over(n, idle) {
					live++
				}
			}
		}
		sh.mu.Unlock()
	}
	return live
}

// Policies returns the policies of the modules and roles of engine that
// the running config has, sorted by name.
func Policies(engine *fib.Engine) []*Policy {
	regMu.Lock()
	defer regMu.Unlock()
	out := make([]*Policy, 0, len(registry[engine]))
	for _, p := range registry[engine] {
		if p.state() != nil {
			out = append(out, p)
		}
	}
	slices.SortFunc(out, func(a, b *Policy) int { return strings.Compare(a.name, b.name) })
	return out
}

// WriteMetrics writes the metrics of the role's policies.
func (r *role) WriteMetrics(w observe.MetricWriter) { WriteMetrics(w, r.key) }

// WriteMetrics writes the metrics of engine's policies: the signed URLs
// each refused, by reason, and its playback sessions playing.
func WriteMetrics(w observe.MetricWriter, engine *fib.Engine) {
	pols := Policies(engine)
	if len(pols) == 0 {
		return
	}
	w.Family("fit_media_auth_url_refused_total", "counter", "Signed playback URLs and grants a media_auth policy refused, by reason: expired, signature, address, referer, origin, session_limit or session_ended.")
	for _, p := range pols {
		for i, reason := range URLReasons {
			w.Sample("fit_media_auth_url_refused_total", float64(p.refused[i].Load()), "policy", p.name, "reason", reason)
		}
	}
	w.Family("fit_media_auth_sessions", "gauge", "Playback sessions of a media_auth policy's signed URLs playing: not idle past session_idle, and not ended.")
	for _, p := range pols {
		w.Sample("fit_media_auth_sessions", float64(p.LiveSessions()), "policy", p.name)
	}
}
