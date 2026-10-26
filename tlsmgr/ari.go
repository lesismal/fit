package tlsmgr

import (
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	mathrand "math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ACME Renewal Information (ARI, RFC 9773): the CA suggests, for each
// certificate, a window to renew it in, which may come earlier than its
// lifetime says when the CA needs it replaced. ACME asks after each run that
// leaves a name with a certificate, and again when the CA's Retry-After says;
// the certificate is due at a time picked at random in the window, or at
// once when the window has begun, and by its lifetime when the CA has no
// ARI or does not answer.

// The bounds of how long ACME waits before it asks about a certificate
// again, and the wait when the CA does not say.
const (
	ariCheckMin     = time.Minute
	ariCheckMax     = 24 * time.Hour
	ariCheckDefault = 6 * time.Hour
	ariMaxBytes     = 64 << 10
)

// ariState is what ACME knows from ARI about a name's certificate.
type ariState struct {
	serial *big.Int
	// renewAt is when the certificate is due, picked in the window; zero
	// when the CA suggested none.
	renewAt time.Time
	// checkAt is when to ask again.
	checkAt time.Time
}

// due reports whether ari says cert is due for renewal at now.
func (s *ariState) due(serial *big.Int, now time.Time) bool {
	return s != nil && s.serial.Cmp(serial) == 0 && !s.renewAt.IsZero() && !now.Before(s.renewAt)
}

// certID is leaf's ARI identifier: its authority key identifier and its
// serial number, each in base64url without padding, joined by a dot; empty
// for a certificate without an authority key identifier.
func certID(leaf *x509.Certificate) string {
	if len(leaf.AuthorityKeyId) == 0 {
		return ""
	}
	serial := leaf.SerialNumber.Bytes()
	if len(serial) == 0 || serial[0]&0x80 != 0 {
		// The DER encoding of a positive INTEGER whose top bit is set.
		serial = append([]byte{0}, serial...)
	}
	return base64.RawURLEncoding.EncodeToString(leaf.AuthorityKeyId) + "." + base64.RawURLEncoding.EncodeToString(serial)
}

// renewalInfoURL returns the CA's ARI endpoint, from its directory, which it
// reads once; empty when the CA has none.
func (a *ACME) renewalInfoURL(ctx context.Context) (string, error) {
	a.mu.Lock()
	u, known := a.ariURL, a.ariKnown
	a.mu.Unlock()
	if known {
		return u, nil
	}
	var dir struct {
		RenewalInfo string `json:"renewalInfo"`
	}
	if err := a.getJSON(ctx, a.directory, &dir, nil); err != nil {
		return "", fmt.Errorf("the directory: %w", err)
	}
	a.mu.Lock()
	a.ariURL, a.ariKnown = strings.TrimRight(dir.RenewalInfo, "/"), true
	a.mu.Unlock()
	return dir.RenewalInfo, nil
}

// getJSON reads the JSON at u into v, and the response's header into h if
// h is not nil.
func (a *ACME) getJSON(ctx context.Context, u string, v any, h *http.Header) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %s", u, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, ariMaxBytes+1))
	if err != nil {
		return err
	}
	if len(data) > ariMaxBytes {
		return errors.New("the answer is too large")
	}
	if h != nil {
		*h = resp.Header
	}
	return json.Unmarshal(data, v)
}

// renewalInfo asks the CA about leaf, and returns what ARI says of it, or
// nil when the CA has no ARI or leaf no identifier for it.
func (a *ACME) renewalInfo(ctx context.Context, leaf *x509.Certificate) (*ariState, error) {
	id := certID(leaf)
	if id == "" {
		return nil, nil
	}
	base, err := a.renewalInfoURL(ctx)
	if err != nil || base == "" {
		return nil, err
	}
	var info struct {
		SuggestedWindow struct {
			Start time.Time `json:"start"`
			End   time.Time `json:"end"`
		} `json:"suggestedWindow"`
	}
	var h http.Header
	if err := a.getJSON(ctx, strings.TrimRight(base, "/")+"/"+id, &info, &h); err != nil {
		return nil, err
	}
	now := a.now()
	s := &ariState{serial: leaf.SerialNumber, checkAt: now.Add(retryAfter(h.Get("Retry-After"), now, a.ariMin, a.ariMax))}
	start, end := info.SuggestedWindow.Start, info.SuggestedWindow.End
	if start.IsZero() || !end.After(start) {
		return s, fmt.Errorf("no valid suggested window: %s to %s", start, end)
	}
	if !now.Before(start) {
		s.renewAt = now
	} else {
		s.renewAt = start.Add(time.Duration(mathrand.Int64N(int64(end.Sub(start)))))
	}
	return s, nil
}

// retryAfter is how long a Retry-After field says to wait, in seconds or as
// a date, kept between least and most; ariCheckDefault when it says nothing
// that parses.
func retryAfter(v string, now time.Time, least, most time.Duration) time.Duration {
	d := ariCheckDefault
	if v != "" {
		if s, err := strconv.Atoi(v); err == nil {
			d = time.Duration(s) * time.Second
		} else if t, err := http.ParseTime(v); err == nil {
			d = t.Sub(now)
		}
	}
	return min(max(d, least), most)
}

// checkARI asks the CA about name's certificate cert when ACME has not, for
// this certificate, or it is time to ask again, and records what it says.
// It is called on a worker, without a.mu.
func (a *ACME) checkARI(ctx context.Context, m *managed, cert *x509.Certificate) {
	a.mu.Lock()
	cur := m.ari
	a.mu.Unlock()
	now := a.now()
	if cur != nil && cur.serial.Cmp(cert.SerialNumber) == 0 && now.Before(cur.checkAt) {
		return
	}
	s, err := a.renewalInfo(ctx, cert)
	if err != nil {
		a.log.Warnf("acme: %s: renewal information: %v", m.name, err)
		if s == nil {
			s = &ariState{serial: cert.SerialNumber, checkAt: now.Add(min(ariCheckDefault, a.ariMax))}
		}
	}
	if s == nil {
		return
	}
	a.mu.Lock()
	m.ari = s
	a.mu.Unlock()
}
