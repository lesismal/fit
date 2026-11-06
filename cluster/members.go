package cluster

import (
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lesismal/fit/internal/certpin"
)

// Section is the store's section the manager keeps its document in: the
// members, their credentials, and what later iterations add beside them.
const Section = "cluster"

// DocVersion is the version of the document this fit writes. A document of
// an earlier version is read and written back as this one; one of a later
// version, which a newer fit wrote, is read for its members and never
// written over: the manager is then read-only.
//
// Version 2 (I149) added the members that link out to the manager: a
// member's link, and the link listener's certificate.
const DocVersion = 2

// The ways a manager reaches a member.
const (
	// AccessConsole logs in to the member's console with a user name and
	// a password, and keeps the session's cookie and CSRF token; the
	// console passes the Admin API on at /api/m/admin/.
	AccessConsole = "console"
	// AccessAdminAPI calls the member's Admin API with its bearer token.
	AccessAdminAPI = "admin_api"
	// AccessLink reaches a member that links out to the manager
	// (admin.cluster_member on the member) through its link: the
	// member's Admin API, served in process, over a WebSocket the member
	// opened. Such a member is added by enrollment, not by its URL.
	AccessLink = "link"
)

// The bounds of what a member holds.
const (
	MaxNameBytes     = 64
	MaxLabels        = 32
	MaxLabelKey      = 63
	MaxLabelValue    = 63
	MaxURLBytes      = 2048
	MaxUsernameBytes = 128
	MaxPasswordBytes = 1024
	MaxTokenBytes    = 4096
	MaxCABytes       = 64 << 10
	// MaxAudit is how many audit entries the document keeps, the oldest
	// dropped first.
	MaxAudit = 1000
	// MaxBodyBytes bounds a request's body to the module.
	MaxBodyBytes = 128 << 10
)

// TLS is how a member's certificate is verified: by the system's CAs when
// both are empty, by CA, a PEM of the CAs to trust, or by Fingerprint, the
// SHA-256 of the member's leaf certificate, pinned once a person confirmed
// it.
type TLS struct {
	CA          string `json:"ca,omitempty"`
	Fingerprint string `json:"fingerprint,omitempty"`
}

// Verified names what verifies a member's certificate: "pin", "ca" or
// "system".
func (t TLS) Verified() string {
	switch {
	case t.Fingerprint != "":
		return "pin"
	case t.CA != "":
		return "ca"
	}
	return "system"
}

// Member is a fit the manager manages, as the store keeps it: its name,
// labels, URL and access, with the credentials, which never leave the
// manager.
type Member struct {
	Name     string            `json:"name"`
	Labels   map[string]string `json:"labels,omitempty"`
	URL      string            `json:"url"`
	Access   string            `json:"access"`
	Username string            `json:"username,omitempty"`
	Password string            `json:"password,omitempty"`
	Token    string            `json:"token,omitempty"`
	TLS      TLS               `json:"tls,omitzero"`
	// Link is a member's link, for AccessLink.
	Link    *Link     `json:"link,omitempty"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

// Link is what the manager keeps of a member that links out to it
// (AccessLink): its credential, made when it enrolled, and the enrollment
// token waiting for it, each only as the hash its proofs are checked with
// (package linktoken).
type Link struct {
	// CredentialID and CredentialHash are the member's credential, which
	// it links with; Enrolled is when it was made.
	CredentialID   string    `json:"credential_id,omitempty"`
	CredentialHash string    `json:"credential_hash,omitempty"`
	Enrolled       time.Time `json:"enrolled,omitzero"`
	// TokenID and TokenHash are the enrollment token made for it, used
	// once, until TokenExpires; TokenMade is when it was made.
	TokenID      string    `json:"token_id,omitempty"`
	TokenHash    string    `json:"token_hash,omitempty"`
	TokenMade    time.Time `json:"token_made,omitzero"`
	TokenExpires time.Time `json:"token_expires,omitzero"`
	// UsedTokenID is the token the member last enrolled with, refused as
	// used from then on.
	UsedTokenID string `json:"used_token_id,omitempty"`
}

// LinkView is what the API answers of a member's link: whether it has a
// credential, and its enrollment token's dates; never a hash.
type LinkView struct {
	Enrolled      time.Time `json:"enrolled,omitzero"`
	CredentialSet bool      `json:"credential_set"`
	// TokenWaiting is an enrollment token made and not used yet, which
	// expires at TokenExpires; TokenExpired says it did.
	TokenWaiting bool      `json:"token_waiting"`
	TokenMade    time.Time `json:"token_made,omitzero"`
	TokenExpires time.Time `json:"token_expires,omitzero"`
}

// view returns what the API shows of l, at now.
func (l *Link) view() *LinkView {
	if l == nil {
		return nil
	}
	return &LinkView{Enrolled: l.Enrolled, CredentialSet: l.CredentialHash != "", TokenWaiting: l.TokenHash != "",
		TokenMade: l.TokenMade, TokenExpires: l.TokenExpires}
}

// MemberView is what the API answers of a member: its credentials only as
// whether they are set.
type MemberView struct {
	Name        string            `json:"name"`
	Labels      map[string]string `json:"labels"`
	URL         string            `json:"url"`
	Access      string            `json:"access"`
	Username    string            `json:"username,omitempty"`
	PasswordSet bool              `json:"password_set"`
	TokenSet    bool              `json:"token_set"`
	TLS         TLS               `json:"tls"`
	// Link is a linked member's (AccessLink).
	Link    *LinkView `json:"link,omitempty"`
	Created time.Time `json:"created"`
	Updated time.Time `json:"updated"`
}

// View returns what the API shows of m.
func (m *Member) View() MemberView {
	labels := maps.Clone(m.Labels)
	if labels == nil {
		labels = map[string]string{}
	}
	return MemberView{Name: m.Name, Labels: labels, URL: m.URL, Access: m.Access, Username: m.Username,
		PasswordSet: m.Password != "", TokenSet: m.Token != "", TLS: m.TLS, Link: m.Link.view(), Created: m.Created, Updated: m.Updated}
}

// clone returns a copy of m that shares nothing with it.
func (m Member) clone() Member {
	m.Labels = maps.Clone(m.Labels)
	if m.Link != nil {
		l := *m.Link
		m.Link = &l
	}
	return m
}

// credentialID is the ID of a linked member's credential, or "".
func (m *Member) credentialID() string {
	if m.Link == nil {
		return ""
	}
	return m.Link.CredentialID
}

// sameLink reports whether a and b reach the member the same way: the same
// URL, access, credentials and TLS, so that a session and a connection of
// one serve the other.
func sameLink(a, b *Member) bool {
	return a.URL == b.URL && a.Access == b.Access && a.Username == b.Username && a.Password == b.Password &&
		a.Token == b.Token && a.TLS == b.TLS && a.credentialID() == b.credentialID()
}

// Input is a member as the API takes it, to add or replace one: a password
// or token left empty keeps the one kept, when there is one.
type Input struct {
	Name     string            `json:"name"`
	Labels   map[string]string `json:"labels"`
	URL      string            `json:"url"`
	Access   string            `json:"access"`
	Username string            `json:"username"`
	Password string            `json:"password"`
	Token    string            `json:"token"`
	TLS      TLS               `json:"tls"`
}

// FieldError is a member refused for one of its fields.
type FieldError struct {
	Field string `json:"field"`
	Msg   string `json:"error"`
}

func (e *FieldError) Error() string { return e.Field + ": " + e.Msg }

func fieldErr(field, format string, args ...any) error {
	return &FieldError{Field: field, Msg: fmt.Sprintf(format, args...)}
}

var (
	namePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	labelPattern = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9_./-]*[A-Za-z0-9])?$`)
)

// member returns the member in, taking the credentials old keeps where in
// leaves them empty (old is nil for a new member), checked; now stamps it.
func (in *Input) member(old *Member, now time.Time) (Member, error) {
	m := Member{Name: strings.TrimSpace(in.Name), URL: strings.TrimSpace(in.URL), Access: in.Access,
		Username: in.Username, Password: in.Password, Token: in.Token,
		TLS: TLS{CA: strings.TrimSpace(in.TLS.CA), Fingerprint: strings.TrimSpace(in.TLS.Fingerprint)}, Created: now, Updated: now}
	if len(in.Labels) > 0 {
		m.Labels = maps.Clone(in.Labels)
	}
	switch {
	case m.Access != "":
	case old != nil && old.Access == AccessLink:
		m.Access = AccessLink
	default:
		m.Access = AccessConsole
	}
	if m.Access == AccessLink {
		// A linked member keeps its link; only its name and labels
		// change here.
		if old == nil || old.Access != AccessLink {
			return Member{}, fieldErr("access", "a member that links out to the manager is added by enrollment, not by its URL")
		}
		m.URL, m.Username, m.Password, m.Token, m.TLS = "", "", "", "", TLS{}
		m.Created, m.Link = old.Created, old.clone().Link
		if err := m.check(); err != nil {
			return Member{}, err
		}
		return m, nil
	}
	if old != nil {
		m.Created = old.Created
		if m.Password == "" && m.Access == AccessConsole && old.Access == AccessConsole {
			m.Password = old.Password
		}
		if m.Token == "" && m.Access == AccessAdminAPI && old.Access == AccessAdminAPI {
			m.Token = old.Token
		}
	}
	// A credential of the other access is not kept.
	switch m.Access {
	case AccessConsole:
		m.Token = ""
	case AccessAdminAPI:
		m.Username, m.Password = "", ""
	}
	if err := m.check(); err != nil {
		return Member{}, err
	}
	if m.TLS.Fingerprint != "" {
		pin, _ := certpin.Parse(m.TLS.Fingerprint) // checked
		m.TLS.Fingerprint = formatFingerprint(pin)
	}
	return m, nil
}

// check checks m as a member the manager can reach.
func (m *Member) check() error {
	switch {
	case m.Name == "" || len(m.Name) > MaxNameBytes || !namePattern.MatchString(m.Name):
		return fieldErr("name", "%q is not a name: 1 to %d letters, digits, '_', '.' and '-', starting with a letter or digit", m.Name, MaxNameBytes)
	case len(m.Labels) > MaxLabels:
		return fieldErr("labels", "%d labels, more than %d", len(m.Labels), MaxLabels)
	}
	for _, k := range slices.Sorted(maps.Keys(m.Labels)) {
		v := m.Labels[k]
		if len(k) > MaxLabelKey || !labelPattern.MatchString(k) {
			return fieldErr("labels", "%q is not a label: 1 to %d letters, digits, '_', '.', '/' and '-', starting and ending with a letter or digit", k, MaxLabelKey)
		}
		if len(v) > MaxLabelValue || v != "" && !labelPattern.MatchString(v) {
			return fieldErr("labels", "the value of %q is not 0 to %d letters, digits, '_', '.', '/' and '-', starting and ending with a letter or digit", k, MaxLabelValue)
		}
	}
	if m.Access == AccessLink {
		if m.Link == nil || m.Link.CredentialHash == "" && m.Link.TokenHash == "" {
			return fieldErr("link", "the linked member has neither a credential nor an enrollment token: make it a token")
		}
		if m.URL != "" || m.Username != "" || m.Password != "" || m.Token != "" || m.TLS != (TLS{}) {
			return fieldErr("access", "a linked member has no URL, credentials or TLS of its own: it is reached through its link")
		}
		return nil
	}
	if err := checkURL(m.URL); err != nil {
		return err
	}
	switch m.Access {
	case AccessConsole:
		switch {
		case m.Username == "" || len(m.Username) > MaxUsernameBytes || !printable(m.Username):
			return fieldErr("username", "the console's user name is 1 to %d printable characters", MaxUsernameBytes)
		case m.Password == "" || len(m.Password) > MaxPasswordBytes || !utf8.ValidString(m.Password):
			return fieldErr("password", "the console user's password is 1 to %d bytes of UTF-8", MaxPasswordBytes)
		}
	case AccessAdminAPI:
		if m.Token == "" || len(m.Token) > MaxTokenBytes || !printable(m.Token) || strings.ContainsRune(m.Token, ' ') {
			return fieldErr("token", "the Admin API's token is 1 to %d printable characters without spaces", MaxTokenBytes)
		}
	default:
		return fieldErr("access", "%q is not an access: %s or %s (or %s, added by enrollment)", m.Access, AccessConsole, AccessAdminAPI, AccessLink)
	}
	if m.TLS.CA != "" && m.TLS.Fingerprint != "" {
		return fieldErr("tls", "ca and fingerprint are two ways to trust the member's certificate: set one")
	}
	if m.TLS.CA != "" {
		if len(m.TLS.CA) > MaxCABytes {
			return fieldErr("tls.ca", "the CAs are more than %d bytes", MaxCABytes)
		}
		if _, err := caPool(m.TLS.CA); err != nil {
			return fieldErr("tls.ca", "%v", err)
		}
	}
	if m.TLS.Fingerprint != "" {
		if _, err := certpin.Parse(m.TLS.Fingerprint); err != nil {
			return fieldErr("tls.fingerprint", "%v", err)
		}
	}
	if (m.TLS.CA != "" || m.TLS.Fingerprint != "") && !strings.HasPrefix(m.URL, "https://") {
		return fieldErr("tls", "a member reached over http has no certificate to verify")
	}
	return nil
}

// checkURL checks a member's URL: https, or http to a loopback address,
// whose credentials do not cross a network; a path, no query, no
// user.
func checkURL(raw string) error {
	if raw == "" || len(raw) > MaxURLBytes {
		return fieldErr("url", "the URL is 1 to %d bytes", MaxURLBytes)
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return fieldErr("url", "%q is not an http or https URL without a user, a query or a fragment", raw)
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !loopback(u.Hostname()) {
			return fieldErr("url", "%q is http to a host that is not a loopback address: use https, so that credentials do not cross a network in the clear", raw)
		}
	default:
		return fieldErr("url", "%q is not an http or https URL", raw)
	}
	return nil
}

func loopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func printable(s string) bool {
	for _, r := range s {
		if r < ' ' || r == 0x7f || !utf8.ValidRune(r) {
			return false
		}
	}
	return utf8.ValidString(s)
}

// caPool returns the CAs of pemData, at least one.
func caPool(pemData string) (*x509.CertPool, error) {
	pool := x509.NewCertPool()
	rest, n := []byte(pemData), 0
	for {
		var b *pem.Block
		if b, rest = pem.Decode(rest); b == nil {
			break
		}
		if b.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(b.Bytes)
		if err != nil {
			return nil, fmt.Errorf("a certificate does not parse: %v", err)
		}
		pool.AddCert(c)
		n++
	}
	if n == 0 {
		return nil, errors.New("no PEM certificate")
	}
	return pool, nil
}

// formatFingerprint writes a SHA-256 as fit writes pins: lower-case hex,
// its bytes apart by colons, as browsers show them.
func formatFingerprint(sum []byte) string {
	var b strings.Builder
	for i, c := range sum {
		if i > 0 {
			b.WriteByte(':')
		}
		fmt.Fprintf(&b, "%02x", c)
	}
	return b.String()
}

// AuditEntry is a change of the manager's, kept in its document: who made
// it, when, what and to which member. Detail never holds a credential.
type AuditEntry struct {
	At     time.Time `json:"at"`
	User   string    `json:"user"`
	Action string    `json:"action"`
	Node   string    `json:"node,omitempty"`
	// Group is the group changed, or the group an operation ran on.
	Group  string `json:"group,omitempty"`
	Detail string `json:"detail,omitempty"`
	// Op is an operation's ID, and Outcomes how it went on each member.
	Op       string         `json:"op,omitempty"`
	Outcomes []AuditOutcome `json:"outcomes,omitempty"`
}

// AuditOutcome is how an operation went on one member: its status, done or
// failed, what it did, and why it failed, bounded.
type AuditOutcome struct {
	Node   string `json:"node"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
	Error  string `json:"error,omitempty"`
}

// The actions an AuditEntry records.
const (
	ActionAdd    = "node.add"
	ActionUpdate = "node.update"
	ActionRemove = "node.remove"
	// ActionEnrollToken is an enrollment token made for a member that
	// links out; ActionEnrolled the member's use of it.
	ActionEnrollToken = "node.enroll_token"
	ActionEnrolled    = "node.enrolled"
)

// document is the store's section: the members, the groups (I144), and
// what I145 to I148 keep beside them, which this version keeps as it finds
// it.
type document struct {
	Version   int               `json:"version"`
	Members   []Member          `json:"members"`
	Groups    []Group           `json:"groups"`
	Templates []json.RawMessage `json:"templates"`
	Rollouts  []json.RawMessage `json:"rollouts"`
	Audit     []AuditEntry      `json:"audit"`
	// LinkCert is the link listener's certificate and key, PEM, made once
	// and kept, so that the members' pins of it stay good.
	LinkCert *linkCert `json:"link_cert,omitempty"`
}

// linkCert is a certificate and its key, PEM.
type linkCert struct {
	Cert string `json:"cert"`
	Key  string `json:"key"`
}

// readDocument reads the store's section, or an empty document for none.
// newer reports a document of a version after DocVersion, which is read for
// its members and must not be written over.
func readDocument(data []byte) (d document, newer bool, err error) {
	if len(data) == 0 {
		return document{Version: DocVersion}, false, nil
	}
	if err := json.Unmarshal(data, &d); err != nil {
		return document{Version: DocVersion}, false, fmt.Errorf("the store's section %q is not the manager's document: %v", Section, err)
	}
	// Version 0, a document that names none, is version 1's shape.
	return d, d.Version > DocVersion, nil
}

// marshal returns d as the store keeps it, as version DocVersion.
func (d *document) marshal() []byte {
	out := *d
	out.Version = DocVersion
	if out.Groups == nil {
		out.Groups = []Group{}
	}
	for _, p := range []*[]json.RawMessage{&out.Templates, &out.Rollouts} {
		if *p == nil {
			*p = []json.RawMessage{}
		}
	}
	if out.Members == nil {
		out.Members = []Member{}
	}
	if out.Audit == nil {
		out.Audit = []AuditEntry{}
	}
	data, _ := json.Marshal(out) // strings, times and JSON values
	return data
}
