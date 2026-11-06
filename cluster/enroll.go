package cluster

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/lesismal/fit/app/console"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/internal/linktoken"
)

// MinEnrollTTL is the shortest lifetime of an enrollment token.
const MinEnrollTTL = time.Minute

// EnrollInput is what Add by enrollment takes: the member's name and
// labels (for a member enrolled again, only TTL and URL count), the
// token's lifetime, as 24h or 7d (config.DefaultClusterEnrollTTL when
// empty, config.MaxClusterEnrollTTL at most), and the URL the member's
// config names the manager by, when not the one LinkInfo gives.
type EnrollInput struct {
	Name   string            `json:"name"`
	Labels map[string]string `json:"labels"`
	TTL    string            `json:"ttl"`
	URL    string            `json:"url"`
}

// Enrollment is an enrollment token made: shown once, never kept but as a
// hash. Config is admin.cluster_member as the member's config takes it, and
// ConfigLine the same as a line to paste into its admin.
type Enrollment struct {
	Node       MemberView            `json:"node"`
	Token      string                `json:"token"`
	Expires    time.Time             `json:"expires"`
	Config     *config.ClusterMember `json:"config"`
	ConfigLine string                `json:"config_line"`
}

// errNoLinks is an enrollment on a manager without a link listener.
var errNoLinks = errConflict("the manager takes no links: set admin.cluster.link_address, and restart it")

// ttl reads in's lifetime.
func (in *EnrollInput) ttl() (time.Duration, error) {
	if strings.TrimSpace(in.TTL) == "" {
		return config.DefaultClusterEnrollTTL.Std(), nil
	}
	d, err := linktoken.ParseTTL(strings.TrimSpace(in.TTL))
	if err != nil {
		return 0, fieldErr("ttl", "%v", err)
	}
	if d < MinEnrollTTL || d > config.MaxClusterEnrollTTL.Std() {
		return 0, fieldErr("ttl", "an enrollment token lasts %s to %s", MinEnrollTTL, config.MaxClusterEnrollTTL.Std())
	}
	return d, nil
}

// enrollment returns what a token secret made for mem gives the person who
// made it; host is where the page was opened.
func (m *Manager) enrollment(mem *Member, secret linktoken.Secret, url, host string) Enrollment {
	info := m.LinkInfo(host)
	if url == "" {
		url = info.URL
	}
	cm := &config.ClusterMember{Manager: url, Fingerprint: info.Fingerprint, Token: secret.StringAs(config.ClusterEnrollPrefix)}
	line, _ := json.Marshal(cm) // strings
	return Enrollment{Node: mem.View(), Token: cm.Token, Expires: mem.Link.TokenExpires, Config: cm,
		ConfigLine: `"cluster_member": ` + string(line)}
}

// checkEnroll checks what an enrollment needs beside its member: links
// taken, a lifetime, a URL.
func (m *Manager) checkEnroll(in *EnrollInput) (time.Duration, error) {
	if m.link.ln == nil {
		return 0, errNoLinks
	}
	d, err := in.ttl()
	if err != nil {
		return 0, err
	}
	in.URL = strings.TrimSpace(in.URL)
	if in.URL != "" {
		if msg := config.CheckClusterLinkURL(in.URL); msg != "" {
			return 0, fieldErr("url", "%s", msg)
		}
	}
	return d, nil
}

// Enroll adds a member that links out to the manager, as user, with an
// enrollment token for it to link with once; host is where the page was
// opened, which the config line names the manager by when link_url does
// not. It writes the store: on the console's pool.
func (m *Manager) Enroll(user string, in *EnrollInput, host string) (Enrollment, error) {
	ttl, err := m.checkEnroll(in)
	if err != nil {
		return Enrollment{}, err
	}
	secret := linktoken.New()
	var mem Member
	err = m.change(user, func(d *document) (AuditEntry, error) {
		now := m.now().UTC()
		mem = Member{Name: strings.TrimSpace(in.Name), Access: AccessLink, Created: now, Updated: now,
			Link: &Link{TokenID: secret.ID, TokenHash: secret.Hash(), TokenMade: now, TokenExpires: now.Add(ttl)}}
		if len(in.Labels) > 0 {
			mem.Labels = in.Labels
		}
		if err := mem.check(); err != nil {
			return AuditEntry{}, err
		}
		if index(d, mem.Name) >= 0 {
			return AuditEntry{}, errConflict("a member is already named " + strconv.Quote(mem.Name))
		}
		if len(d.Members) >= m.maxNodes {
			return AuditEntry{}, errConflict(fmt.Sprintf("the manager keeps %d members at most (max_nodes)", m.maxNodes))
		}
		d.Members = append(d.Members, mem)
		return AuditEntry{Action: ActionEnrollToken, Node: mem.Name, Detail: "a new member; the token expires at " + mem.Link.TokenExpires.Format(time.RFC3339)}, nil
	})
	if err != nil {
		return Enrollment{}, err
	}
	m.logf("cluster: %q added the member %q by enrollment, its token expiring at %s", user, mem.Name, mem.Link.TokenExpires.Format(time.RFC3339))
	return m.enrollment(&mem, secret, in.URL, host), nil
}

// EnrollAgain makes the linked member name a new enrollment token, as user,
// in place of any it had: the member keeps its credential, and links with
// it, until it enrolls with the new token, which then replaces it. For a
// member that lost its credential, or moved to another host.
func (m *Manager) EnrollAgain(user, name string, in *EnrollInput, host string) (Enrollment, error) {
	ttl, err := m.checkEnroll(in)
	if err != nil {
		return Enrollment{}, err
	}
	secret := linktoken.New()
	var mem Member
	err = m.change(user, func(d *document) (AuditEntry, error) {
		i := index(d, name)
		if i < 0 {
			return AuditEntry{}, errNotFound(name)
		}
		if d.Members[i].Access != AccessLink {
			return AuditEntry{}, errConflict(strconv.Quote(name) + " is reached at its URL, not through a link: only a member that links out is enrolled")
		}
		now := m.now().UTC()
		l := d.Members[i].Link
		l.TokenID, l.TokenHash, l.TokenMade, l.TokenExpires = secret.ID, secret.Hash(), now, now.Add(ttl)
		d.Members[i].Updated = now
		mem = d.Members[i].clone()
		return AuditEntry{Action: ActionEnrollToken, Node: name, Detail: "the token expires at " + l.TokenExpires.Format(time.RFC3339)}, nil
	})
	if err != nil {
		return Enrollment{}, err
	}
	m.logf("cluster: %q made the member %q a new enrollment token, expiring at %s", user, name, mem.Link.TokenExpires.Format(time.RFC3339))
	return m.enrollment(&mem, secret, in.URL, host), nil
}

// linkRoutes adds the endpoints of the members that link out: where they
// link to, and their enrollment.
func (m *Manager) linkRoutes(api *console.API) {
	api.Handle("GET", "/api/link", func(r *console.Request) { _ = r.JSON(200, m.LinkInfo(r.Request.Host)) })
	api.Handle("POST", "/api/enroll", m.enrollNew)
}

func decodeEnroll(r *console.Request, in *EnrollInput) bool {
	if len(r.Context.Body()) > MaxBodyBytes {
		_ = console.Error(r.Context, 413, fmt.Sprintf("an enrollment is %d bytes at most", MaxBodyBytes))
		return false
	}
	return r.Decode(in)
}

// enrollNew answers POST /api/enroll.
func (m *Manager) enrollNew(r *console.Request) {
	var in EnrollInput
	if !decodeEnroll(r, &in) || !m.ifMatch(r) {
		return
	}
	user, host := r.User, r.Request.Host
	r.Go(func() {
		e, err := m.Enroll(user, &in, host)
		m.answer(r, 201, e, err)
	})
}

// enrollAgain answers POST /api/nodes/<name>/enroll.
func (m *Manager) enrollAgain(r *console.Request, name string) {
	var in EnrollInput
	if len(r.Context.Body()) > 0 && !decodeEnroll(r, &in) || !m.ifMatch(r) {
		return
	}
	user, host := r.User, r.Request.Host
	r.Go(func() {
		e, err := m.EnrollAgain(user, name, &in, host)
		m.answer(r, 200, e, err)
	})
}
