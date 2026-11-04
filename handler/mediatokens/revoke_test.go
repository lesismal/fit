package mediatokens

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lesismal/fit/mediaauth"
)

// TestRevoke: a revoke writes a new secret of at least 16 bytes to the
// policy's secret_file, rotates the running policy so that an old token is
// refused and a new one is accepted, and a policy rebuilt from the rewritten
// file accepts the new tokens and refuses the old ones.
func TestRevoke(t *testing.T) {
	f := startFit(t, newSecretFile(t), "/media-tokens", `{"auth": "live"}`)
	old := mustIssue(t, f, `{"subject": "a", "publish": ["mtg-*"], "play": ["mtg-*"]}`)[0].Token
	live := f.policy(t, "live")
	if err := allowed(live, mediaauth.Publish, "mtg-1", old); err != nil {
		t.Fatalf("the old token before the revoke: %v", err)
	}
	if err := allowed(f.fromFile(t), mediaauth.Publish, "mtg-1", old); err != nil {
		t.Fatalf("the old token against a policy from the file: %v", err)
	}

	before := time.Now().Unix()
	resp, body := revoke(t, f)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("revoke: %d %s", resp.StatusCode, body)
	}
	var out struct {
		Revoked bool  `json:"revoked"`
		Since   int64 `json:"since"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil || !out.Revoked {
		t.Fatalf("revoke: %v: %s", err, body)
	}
	near(t, out.Since, before, 5*time.Second)

	// The file holds a new secret, of at least 16 bytes, written 0600.
	b, err := os.ReadFile(f.secretFile)
	if err != nil {
		t.Fatal(err)
	}
	secret := strings.TrimRight(string(b), "\r\n")
	if len(secret) < 16 {
		t.Fatalf("the secret file holds %d bytes: %q", len(secret), secret)
	}
	if secret == oldSecret {
		t.Fatal("the secret file holds the old secret")
	}
	info, err := os.Stat(f.secretFile)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("the secret file's mode is %o, want 600", perm)
	}

	// The running policy refuses the old token, and accepts a token signed
	// after the revoke.
	if err := allowed(live, mediaauth.Publish, "mtg-1", old); !errors.Is(err, mediaauth.ErrBadCredentials) {
		t.Fatalf("the old token after the revoke: %v, want bad credentials", err)
	}
	newer := mustIssue(t, f, `{"subject": "b", "publish": ["mtg-*"], "play": ["mtg-*"]}`)[0].Token
	if err := allowed(live, mediaauth.Publish, "mtg-1", newer); err != nil {
		t.Fatalf("a token signed after the revoke: %v", err)
	}
	// A policy rebuilt from the rewritten file, as a restart reads it,
	// accepts the token signed after the revoke and refuses the old one: the
	// revocation outlives the process.
	after := f.fromFile(t)
	if err := allowed(after, mediaauth.Publish, "mtg-1", newer); err != nil {
		t.Fatalf("a token signed after the revoke, against a policy from the file: %v", err)
	}
	if err := allowed(after, mediaauth.Publish, "mtg-1", old); !errors.Is(err, mediaauth.ErrBadCredentials) {
		t.Fatalf("the old token against a policy from the rewritten file: %v, want bad credentials", err)
	}
}

// TestRevokeWriteFailure: a secret file that cannot be written is a 500, and
// rotates nothing.
func TestRevokeWriteFailure(t *testing.T) {
	f := startFit(t, newSecretFile(t), "/media-tokens", `{"auth": "live"}`)
	old := mustIssue(t, f, `{"subject": "a", "publish": ["mtg-*"]}`)[0].Token
	live := f.policy(t, "live")
	// The write renames over the secret file; a directory there makes the
	// rename fail, as an unwritable path would.
	if err := os.Remove(f.secretFile); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(f.secretFile, 0o755); err != nil {
		t.Fatal(err)
	}
	resp, body := revoke(t, f)
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("a revoke that cannot write: %d %s", resp.StatusCode, body)
	}
	if err := allowed(live, mediaauth.Publish, "mtg-1", old); err != nil {
		t.Fatalf("a failed revoke rotated the policy: %v", err)
	}
}

// TestRevokePoolIsBusy: with a revoke's write under way and another waiting,
// the next revoke is 503 at once, with Retry-After; the waiting one is
// answered once the write ends.
func TestRevokePoolIsBusy(t *testing.T) {
	f := startFit(t, newSecretFile(t), "/media-tokens", `{"auth": "live", "revoke_timeout": "10s"}`)
	h := f.handler(t)
	release := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	h.pool.setHook(func() {
		once.Do(func() { close(started) })
		<-release
	})
	type result struct {
		code int
		body string
		err  error
	}
	answers := make(chan result, 2)
	go func() {
		resp, body, err := send("POST", f.base+"/media-tokens/revoke", "", nil)
		if err != nil {
			answers <- result{err: err}
			return
		}
		answers <- result{code: resp.StatusCode, body: body}
	}()
	select {
	case <-started:
	case <-time.After(waitTimeout):
		t.Fatal("the first revoke's write did not start")
	}
	go func() {
		resp, body, err := send("POST", f.base+"/media-tokens/revoke", "", nil)
		if err != nil {
			answers <- result{err: err}
			return
		}
		answers <- result{code: resp.StatusCode, body: body}
	}()
	// The second revoke waits in the pool's queue of one.
	deadline := time.Now().Add(waitTimeout)
	for len(h.pool.jobs) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("the second revoke was not queued")
		}
		time.Sleep(time.Millisecond)
	}
	resp, body := revoke(t, f)
	if resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") != "1" {
		t.Fatalf("a revoke with the pool busy: %d %s, Retry-After %q", resp.StatusCode, body, resp.Header.Get("Retry-After"))
	}
	close(release)
	for i := range 2 {
		select {
		case r := <-answers:
			if r.err != nil {
				t.Fatalf("queued revoke %d: %v", i, r.err)
			}
			if r.code != http.StatusOK {
				t.Errorf("queued revoke %d: %d %s", i, r.code, r.body)
			}
		case <-time.After(waitTimeout):
			t.Fatalf("queued revoke %d: no answer", i)
		}
	}
}

// TestRevokeTimeout: a write not done within revoke_timeout is answered 504;
// the write, once it can go on, still ends, and the policy is rotated with
// the file.
func TestRevokeTimeout(t *testing.T) {
	f := startFit(t, newSecretFile(t), "/media-tokens", `{"auth": "live", "revoke_timeout": "200ms"}`)
	old := mustIssue(t, f, `{"subject": "a", "publish": ["mtg-*"]}`)[0].Token
	live := f.policy(t, "live")
	h := f.handler(t)
	release := make(chan struct{})
	started := make(chan struct{})
	var once sync.Once
	h.pool.setHook(func() {
		once.Do(func() { close(started) })
		<-release
	})
	start := time.Now()
	resp, body := revoke(t, f)
	elapsed := time.Since(start)
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("a revoke past revoke_timeout: %d %s", resp.StatusCode, body)
	}
	if elapsed < 150*time.Millisecond || elapsed > 5*time.Second {
		t.Fatalf("a revoke past revoke_timeout answered after %v", elapsed)
	}
	select {
	case <-started:
	case <-time.After(waitTimeout):
		t.Fatal("the revoke's write did not start")
	}
	close(release)
	// The time-out left the write's outcome unknown; it ends as the file
	// says, and the policy is rotated with it.
	deadline := time.Now().Add(waitTimeout)
	for {
		b, err := os.ReadFile(f.secretFile)
		if err == nil && strings.TrimRight(string(b), "\r\n") != oldSecret {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the timed-out write did not end")
		}
		time.Sleep(5 * time.Millisecond)
	}
	for {
		if errors.Is(allowed(live, mediaauth.Publish, "mtg-1", old), mediaauth.ErrBadCredentials) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the policy was not rotated after the write ended")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
