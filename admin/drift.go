package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net"
	"net/http"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	fib "github.com/lesismal/fib"

	"github.com/lesismal/fit/app"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/listener"
	"github.com/lesismal/fit/middleware/cache"
	"github.com/lesismal/fit/provider"
	"github.com/lesismal/fit/runtime"
	"github.com/lesismal/fit/upstream"
)

// The kinds a difference has: drift is an inconsistency the runtime or the
// config can be moved to fix, and info is a known runtime fact that is not
// one.
const (
	kindDrift = "drift"
	kindInfo  = "info"
)

// The actions a fix offers, and the operations its patch is applied with.
const (
	fixAdd     = "add"
	fixDelete  = "delete"
	fixReplace = "replace"
	fixReapply = "reapply"

	patchAdd     = "add"
	patchDelete  = "delete"
	patchReplace = "replace"
)

// The categories differences are listed in, in this order, then by name and
// ID. The names of the members providers provide are categories too, so that
// what a provider provides shows beside the base config's own.
var checkCategories = []string{"settings", "listeners", "routes", "upstreams", "services", "tls", "roles", "cache", "providers"}

// CheckResult is GET /config/check's answer: the differences between the
// running service and the config it runs from, with the version and ETag of
// that config, which POST /config/reconcile takes as If-Match.
type CheckResult struct {
	Version     uint64       `json:"version"`
	ETag        string       `json:"etag"`
	CheckedAt   time.Time    `json:"checked_at"`
	Differences []Difference `json:"differences"`
	Summary     CheckSummary `json:"summary"`
}

// CheckSummary counts the differences by kind.
type CheckSummary struct {
	Drift int `json:"drift"`
	Info  int `json:"info"`
}

// Difference is one place the running service and the config it runs from do
// not agree, or a runtime fact a check reports without a fix.
type Difference struct {
	// ID is stable, as "setting:log" or "role:llm.hub", and names the
	// difference across checks.
	ID       string `json:"id"`
	Category string `json:"category"`
	Kind     string `json:"kind"`
	// Name is the difference's subject, as the config or the runtime
	// writes it.
	Name   string `json:"name"`
	Detail string `json:"detail"`
	// Config and Running are the two sides, when they have a value.
	Config  any `json:"config,omitempty"`
	Running any `json:"running,omitempty"`
	// Fixes are what can be done about a drift, each selected by its
	// action; an info has none.
	Fixes []Fix `json:"fixes,omitempty"`
}

// Fix is one way to make a drift go away: a patch to apply to the base
// config, or, for reapply, applying the running config again.
type Fix struct {
	Action string       `json:"action"`
	Label  string       `json:"label"`
	Patch  *ConfigPatch `json:"patch,omitempty"`
}

// ConfigPatch is an edit of the base config: a JSON pointer to what it
// changes, and the value an add or a replace writes there.
type ConfigPatch struct {
	Op    string          `json:"op"`
	Path  string          `json:"path"`
	Value json.RawMessage `json:"value,omitempty"`
}

// Check returns the differences between the running service and the config
// it runs from, as GET /config/check answers them. It takes the config under
// cs.mu and reads the runtime after letting go of it, so that a config
// applied meanwhile leaves its ETag stale and reconcile's If-Match refuses
// a fix made from it.
func (cs *Configs) Check() CheckResult {
	st := cs.snapshot()
	res := CheckResult{CheckedAt: time.Now(), Differences: []Difference{}}
	if st.cur == nil {
		return res
	}
	res.Version, res.ETag = st.cur.Number, st.cur.ETag
	res.Differences = cs.checkLocked(st)
	for _, d := range res.Differences {
		if d.Kind == kindInfo {
			res.Summary.Info++
		} else {
			res.Summary.Drift++
		}
	}
	return res
}

// checkState is the config a check compares the runtime with: the running
// version, the config it loaded, the config fit started with, whose
// restart-only members are in force, and the roles manager, or nil.
type checkState struct {
	cur   *Version
	cfg   *config.Config
	first *config.Config
	apps  *app.Manager
}

// snapshot returns the state of the running config; it takes cs.mu.
func (cs *Configs) snapshot() checkState {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	return checkState{cur: cs.history[len(cs.history)-1], cfg: cs.currentCfg, first: cs.first, apps: cs.apps}
}

// checkLocked returns the differences between the running service and st's
// config, sorted. It does not lock cs.mu itself: Check calls it with the
// state it took under the lock, and reconcile with the config its edit
// callback runs under it, so that a fix is checked against the config it
// changes.
func (cs *Configs) checkLocked(st checkState) []Difference {
	if st.cur == nil || st.cfg == nil || st.first == nil {
		return []Difference{}
	}
	base, _ := decodeJSON(st.cur.Data())
	diffs := checkSettings(st.first, st.cfg)
	diffs = append(diffs, checkRoles(st.cfg, app.Roles(), runningRoles(st.apps))...)
	diffs = append(diffs, checkProviders(st.cur)...)
	if cs.server != nil {
		diffs = append(diffs, checkLimits(st.cfg, cs.server.Settings())...)
	}
	// The runtime's own data is read here, never through Status, which
	// would take cs.mu again.
	var g *runtime.Graph
	if cs.rt != nil {
		g = cs.rt.Current()
	}
	if g != nil {
		defer g.Release()
		var bound []listener.Addr
		if cs.server != nil {
			bound = cs.server.Addrs()
		}
		diffs = append(diffs, checkListeners(st.cfg, g.Listeners, bound, base)...)
		diffs = append(diffs, checkTLS(st.cfg, servedCertificates(g.Listeners), base)...)
		diffs = append(diffs, checkUpstreams(st.cfg, g.Upstreams(), base)...)
	}
	diffs = append(diffs, checkCache(st.cfg, cache.Zones())...)
	if diffs == nil {
		diffs = []Difference{}
	}
	sortDifferences(diffs)
	return diffs
}

// sortDifferences orders diffs by category, then name and ID, so that checks
// of one config list them the same way.
func sortDifferences(diffs []Difference) {
	slices.SortStableFunc(diffs, func(a, b Difference) int {
		if c := slices.Index(checkCategories, a.Category) - slices.Index(checkCategories, b.Category); c != 0 {
			return c
		}
		if c := strings.Compare(a.Name, b.Name); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
}

// runningRoles returns the IDs of the roles running, or nil without a roles
// manager.
func runningRoles(apps *app.Manager) []string {
	if apps == nil {
		return nil
	}
	return apps.Running()
}

// checkSettings compares the settings read when fit starts, which keep the
// value fit started with until it restarts, with the config's.
func checkSettings(first, cfg *config.Config) []Difference {
	var diffs []Difference
	if !sameAccessLog(first.AccessLog, cfg.AccessLog) {
		diffs = append(diffs, settingDifference("access_log", cfg.AccessLog, first.AccessLog))
	}
	if !sameLog(first.Log, cfg.Log) {
		diffs = append(diffs, settingDifference("log", cfg.Log, first.Log))
	}
	if !sameJSON(first.Telemetry, cfg.Telemetry) {
		diffs = append(diffs, settingDifference("telemetry", cfg.Telemetry, first.Telemetry))
	}
	if !sameJSON(first.Engine, cfg.Engine) {
		diffs = append(diffs, settingDifference("engine", cfg.Engine, first.Engine))
	}
	if !sameACME(first.ACME, cfg.ACME) {
		diffs = append(diffs, settingDifference("acme", cfg.ACME, first.ACME))
	}
	if !sameProviders(first.Providers, cfg.Providers) {
		diffs = append(diffs, settingDifference("providers", cfg.Providers, first.Providers))
	}
	if !sameAdmin(first.Admin, cfg.Admin) {
		diffs = append(diffs, Difference{ID: "setting:admin", Category: "settings", Kind: kindInfo, Name: "admin",
			Detail: "admin comes from the config fit started with; a change to it takes effect when fit restarts",
			Config: memberJSON(cfg.Admin), Running: memberJSON(first.Admin)})
	}
	return diffs
}

// settingDifference returns the difference for a restart-only setting: the
// config's value is current, and the value in force is the one fit started
// with, which a patch to the config brings back.
func settingDifference(name string, current, inForce any) Difference {
	d := Difference{ID: "setting:" + name, Category: "settings", Kind: kindDrift, Name: name,
		Detail: fmt.Sprintf("the config sets %s differently from the value fit started with, which is in force until fit restarts", name),
		Config: memberJSON(current), Running: memberJSON(inForce)}
	fix := Fix{Action: fixReplace, Label: "revert the config to the value in force"}
	if v := memberJSON(inForce); v != nil {
		fix.Patch = &ConfigPatch{Op: patchReplace, Path: "/" + name, Value: v.(json.RawMessage)}
	} else {
		fix.Patch = &ConfigPatch{Op: patchDelete, Path: "/" + name}
	}
	d.Fixes = []Fix{fix}
	return d
}

// checkLimits compares the limits the config sets with the values the
// listeners' engine took when it started: a limit takes effect at start, not
// on a reload.
func checkLimits(cfg *config.Config, s fib.Settings) []Difference {
	limits := []struct {
		field string
		set   bool
		cfg   any
		run   any
	}{
		{"max_conns", cfg.Limits.MaxConns != 0, cfg.Limits.Conns(), s.MaxConns},
		{"max_pending_bytes", cfg.Limits.MaxPendingBytes != 0, cfg.Limits.PendingBytes(), s.MaxPendingBytes},
		{"max_write_queue_bytes", cfg.Limits.MaxWriteQueueBytes != 0, cfg.Limits.QueueBytes(), s.MaxWriteQueueBytes},
	}
	var diffs []Difference
	for _, l := range limits {
		if !l.set || l.cfg == l.run {
			continue
		}
		value, _ := json.Marshal(l.run)
		diffs = append(diffs, Difference{ID: "setting:limits." + l.field, Category: "settings", Kind: kindDrift,
			Name: "limits." + l.field,
			Detail: fmt.Sprintf("the config sets limits.%s=%v, but the engine runs with %v; limits take effect when fit starts",
				l.field, l.cfg, l.run),
			Config: l.cfg, Running: l.run,
			Fixes: []Fix{{Action: fixReplace, Label: "set the config to the value in force",
				Patch: &ConfigPatch{Op: patchReplace, Path: "/limits/" + l.field, Value: value}}}})
	}
	return diffs
}

// checkRoles compares the roles the config sets with the roles running.
func checkRoles(cfg *config.Config, roles []app.Info, running []string) []Difference {
	up := make(map[string]bool, len(running))
	for _, id := range running {
		up[id] = true
	}
	var diffs []Difference
	for _, info := range roles {
		member := info.Member(cfg)
		configured, isUp := memberSet(member), up[info.ID]
		switch {
		case configured && !isUp:
			diffs = append(diffs, Difference{ID: "role:" + info.ID, Category: "roles", Kind: kindDrift, Name: info.ID,
				Detail: fmt.Sprintf("the config sets %s, but the role is not running", info.ID),
				Config: memberJSON(member), Running: false,
				Fixes: []Fix{
					{Action: fixDelete, Label: "remove it from the config",
						Patch: &ConfigPatch{Op: patchDelete, Path: pointerString(info.Path())}},
					{Action: fixReapply, Label: "apply the config again to retry starting it"},
				}})
		case !configured && isUp:
			diffs = append(diffs, Difference{ID: "role:" + info.ID + ":extra", Category: "roles", Kind: kindDrift, Name: info.ID,
				Detail:  fmt.Sprintf("the role %s is running, but the config does not set it", info.ID),
				Running: true,
				Fixes:   []Fix{{Action: fixReapply, Label: "apply the config again to stop it"}}})
		}
	}
	return diffs
}

// checkProviders returns what providers provide that the base config does
// not hold: each element is a drift that can be saved in the config, and
// what a provider's element left out is info.
func checkProviders(cur *Version) []Difference {
	var diffs []Difference
	if tree, err := decodeJSON(cur.Effective()); err == nil {
		seen := make(map[string]bool, len(cur.sources))
		for _, s := range cur.sources {
			if s.Source == cur.baseSource {
				continue
			}
			member := providerMember(s.Path)
			if member == "" || seen[s.Path] {
				continue
			}
			seen[s.Path] = true
			tokens, err := parsePointer(s.Path)
			if err != nil {
				continue
			}
			v, err := lookup(tree, tokens)
			if err != nil {
				continue
			}
			value := encodeJSON(v)
			diffs = append(diffs, Difference{ID: "provider:" + s.Source + ":" + s.Path, Category: member, Kind: kindDrift,
				Name:    elementName(v, s.Path),
				Detail:  fmt.Sprintf("provided by %s and running, but not in the base config", s.Source),
				Running: json.RawMessage(bytes.TrimSpace(value)),
				Fixes: []Fix{{Action: fixAdd, Label: "save it in the config",
					Patch: &ConfigPatch{Op: patchAdd, Path: "/" + member + "/-", Value: value}}}})
		}
	}
	for _, s := range cur.shadowed {
		diffs = append(diffs, Difference{ID: "shadowed:" + s.Source + ":" + s.Name, Category: "providers", Kind: kindInfo,
			Name:   s.Name,
			Detail: fmt.Sprintf("provided by %s, but left out because %s already uses the name", s.Source, s.By)})
	}
	return diffs
}

// providerMember returns the top-level member a source's path names, when a
// provider may provide it, or "" for another member.
func providerMember(path string) string {
	for _, kind := range provider.Kinds {
		if strings.HasPrefix(path, "/"+kind+"/") {
			return kind
		}
	}
	return ""
}

// elementName returns what an element of a merged config is known by: its
// name, for a route its handler's module, or the path itself.
func elementName(v any, path string) string {
	o, ok := v.(*object)
	if !ok {
		return path
	}
	if n, ok := o.get("name"); ok {
		if s, ok := n.(string); ok && s != "" {
			return s
		}
	}
	if h, ok := o.get("handler"); ok {
		if ho, ok := h.(*object); ok {
			if m, ok := ho.get("module"); ok {
				if s, ok := m.(string); ok && s != "" {
					return s
				}
			}
		}
	}
	return path
}

// checkListeners compares the graph's listeners with the addresses the
// server bound, by name.
func checkListeners(cfg *config.Config, graph []*runtime.Listener, bound []listener.Addr, base any) []Difference {
	generated := generatedListeners(cfg)
	boundByName := make(map[string]listener.Addr, len(bound))
	for _, a := range bound {
		boundByName[a.Name] = a
	}
	inGraph := make(map[string]bool, len(graph))
	var diffs []Difference
	for _, l := range graph {
		inGraph[l.Name] = true
		if generated[l.Name] {
			continue
		}
		if _, ok := boundByName[l.Name]; !ok {
			d := Difference{ID: "listener:" + l.Name + ":missing", Category: "listeners", Kind: kindDrift, Name: l.Name,
				Detail: fmt.Sprintf("the config serves listener %s, but no address is bound for it", l.Name),
				Config: l.Address,
				Fixes:  []Fix{{Action: fixReapply, Label: "apply the config again to retry binding it"}}}
			if i := baseIndex(base, "listeners", l.Name); i >= 0 {
				d.Fixes = append([]Fix{{Action: fixDelete, Label: "remove it from the config",
					Patch: &ConfigPatch{Op: patchDelete, Path: "/listeners/" + strconv.Itoa(i)}}}, d.Fixes...)
			}
			diffs = append(diffs, d)
		}
	}
	for _, a := range bound {
		if generated[a.Name] || inGraph[a.Name] {
			continue
		}
		diffs = append(diffs, Difference{ID: "listener:" + a.Name + ":extra", Category: "listeners", Kind: kindDrift, Name: a.Name,
			Detail:  fmt.Sprintf("a listener %s is bound, but the config does not serve it", a.Name),
			Running: a.Addr.String(),
			Fixes:   []Fix{{Action: fixReapply, Label: "apply the config again to stop it"}}})
	}
	for _, l := range graph {
		a, ok := boundByName[l.Name]
		if !ok || generated[l.Name] || !portZero(l.Address) {
			continue
		}
		diffs = append(diffs, Difference{ID: "listener:" + l.Name + ":port", Category: "listeners", Kind: kindInfo, Name: l.Name,
			Detail: fmt.Sprintf("the config writes address %s, whose port is 0, so the kernel chose %s: a dynamic port is deliberate",
				l.Address, a.Addr),
			Config: l.Address, Running: a.Addr.String()})
	}
	return diffs
}

// generatedListeners returns the listeners HTTPS by default made, by name:
// a config that writes none is served on them, and they are expected, not
// differences.
func generatedListeners(cfg *config.Config) map[string]bool {
	if len(cfg.Listeners) > 0 {
		return nil
	}
	listeners, _ := cfg.Effective()
	names := make(map[string]bool, len(listeners))
	for _, l := range listeners {
		names[l.Name] = true
	}
	return names
}

// portZero reports whether address names port 0.
func portZero(address string) bool {
	_, port, err := net.SplitHostPort(address)
	return err == nil && port == "0"
}

// checkTLS compares the names the config's listeners want certificates for
// with the certificates they serve: a name the base config writes and no
// certificate covers is drift, one HTTPS by default added is info while the
// certificate is obtained, and a certificate no name asked for is info too.
func checkTLS(cfg *config.Config, served map[string][]string, base any) []Difference {
	listeners, _ := cfg.Effective()
	var diffs []Difference
	for _, l := range listeners {
		if l.TLS == nil {
			continue
		}
		names := served[l.Name]
		expected := make([]string, 0, len(l.TLS.ACME))
		for _, name := range l.TLS.ACME {
			expected = append(expected, name)
			if slices.ContainsFunc(names, func(have string) bool { return coversCertificate(have, name) }) {
				continue
			}
			if at, ok := baseACMEName(base, l.Name, name); ok {
				diffs = append(diffs, Difference{ID: "tls:" + l.Name + ":" + name, Category: "tls", Kind: kindDrift, Name: name,
					Detail: fmt.Sprintf("the config wants a certificate for %s on listener %s, but none is served for it", name, l.Name),
					Config: name, Running: namesOrNil(names),
					Fixes: []Fix{{Action: fixDelete, Label: "remove the name from the config",
						Patch: &ConfigPatch{Op: patchDelete, Path: "/listeners/" + strconv.Itoa(at.listener) + "/tls/acme/" + strconv.Itoa(at.name)}}}})
				continue
			}
			diffs = append(diffs, Difference{ID: "tls:" + l.Name + ":" + name + ":auto", Category: "tls", Kind: kindInfo, Name: name,
				Detail:  fmt.Sprintf("HTTPS by default added %s to listener %s, and its certificate is being obtained", name, l.Name),
				Running: namesOrNil(names)})
		}
		for _, name := range names {
			if slices.ContainsFunc(expected, func(want string) bool {
				return coversCertificate(name, want) || coversCertificate(want, name)
			}) {
				continue
			}
			diffs = append(diffs, Difference{ID: "tls:" + l.Name + ":served:" + name, Category: "tls", Kind: kindInfo, Name: name,
				Detail: fmt.Sprintf("listener %s serves a certificate for %s, which the config does not ask for: on-demand TLS, or a certificate from a file",
					l.Name, name),
				Running: name})
		}
	}
	return diffs
}

// coversCertificate reports whether a certificate for served covers name:
// the same name, or a "*.suffix" wildcard for one label of it.
func coversCertificate(served, name string) bool {
	served = strings.ToLower(strings.TrimSuffix(served, "."))
	name = strings.ToLower(strings.TrimSuffix(name, "."))
	if served == name {
		return true
	}
	suffix, ok := strings.CutPrefix(served, "*.")
	if !ok {
		return false
	}
	label, ok := strings.CutSuffix(name, "."+suffix)
	return ok && label != "" && !strings.Contains(label, ".")
}

// namesOrNil returns names, or nil when it is empty: an absent runtime-side
// value.
func namesOrNil(names []string) any {
	if len(names) == 0 {
		return nil
	}
	return names
}

// servedCertificates returns the names of the certificates each listener
// serves, by listener name, deduped and sorted, as Status reports them.
func servedCertificates(listeners []*runtime.Listener) map[string][]string {
	out := make(map[string][]string, len(listeners))
	for _, l := range listeners {
		lister, ok := l.Certificates.(certificateLister)
		if !ok {
			continue
		}
		seen := map[string]bool{}
		for _, c := range lister.Certificates() {
			if c.Leaf == nil {
				continue
			}
			names := slices.Clone(c.Leaf.DNSNames)
			for _, ip := range c.Leaf.IPAddresses {
				names = append(names, ip.String())
			}
			if len(names) == 0 {
				names = []string{c.Leaf.Subject.CommonName}
			}
			for _, name := range names {
				if name != "" && !seen[name] {
					seen[name] = true
					out[l.Name] = append(out[l.Name], name)
				}
			}
		}
		slices.Sort(out[l.Name])
	}
	return out
}

// checkUpstreams compares the graph's upstream pools with the config's
// upstreams, by name.
func checkUpstreams(cfg *config.Config, pools []*upstream.Pool, base any) []Difference {
	poolByName := make(map[string]*upstream.Pool, len(pools))
	for _, p := range pools {
		poolByName[p.Name()] = p
	}
	configured := make(map[string]bool, len(cfg.Upstreams))
	for _, u := range cfg.Upstreams {
		configured[u.Name] = true
	}
	var diffs []Difference
	for _, u := range cfg.Upstreams {
		if poolByName[u.Name] != nil {
			continue
		}
		d := Difference{ID: "upstream:" + u.Name + ":missing", Category: "upstreams", Kind: kindDrift, Name: u.Name,
			Detail: fmt.Sprintf("the config sets upstream %s, but no pool of that name is running", u.Name),
			Config: u.Addresses,
			Fixes:  []Fix{{Action: fixReapply, Label: "apply the config again to start it"}}}
		if i := baseIndex(base, "upstreams", u.Name); i >= 0 {
			d.Fixes = append([]Fix{{Action: fixDelete, Label: "remove it from the config",
				Patch: &ConfigPatch{Op: patchDelete, Path: "/upstreams/" + strconv.Itoa(i)}}}, d.Fixes...)
		}
		diffs = append(diffs, d)
	}
	for _, p := range pools {
		if !configured[p.Name()] {
			diffs = append(diffs, Difference{ID: "upstream:" + p.Name() + ":extra", Category: "upstreams", Kind: kindDrift, Name: p.Name(),
				Detail:  fmt.Sprintf("upstream %s is running, but the config does not set it", p.Name()),
				Running: p.Addresses(),
				Fixes:   []Fix{{Action: fixReapply, Label: "apply the config again to stop it"}}})
			continue
		}
		backends := p.Backends()
		down, states := len(backends) > 0, make([]backendState, len(backends))
		for i, b := range backends {
			states[i] = backendState{Address: b.Address, Up: !b.Down(), InFlight: b.Active()}
			if !b.Down() {
				down = false
			}
		}
		if down {
			diffs = append(diffs, Difference{ID: "upstream:" + p.Name() + ":down", Category: "upstreams", Kind: kindInfo, Name: p.Name(),
				Detail:  fmt.Sprintf("every backend of upstream %s is out of rotation", p.Name()),
				Running: states})
		}
	}
	return diffs
}

// backendState is an upstream backend as a check reports it.
type backendState struct {
	Address  string `json:"address"`
	Up       bool   `json:"up"`
	InFlight int64  `json:"in_flight"`
}

// checkCache compares the cache zones the routes use with the zones the
// cache middleware keeps at runtime.
func checkCache(cfg *config.Config, zones []cache.ZoneStats) []Difference {
	inUse := map[string]bool{}
	for _, r := range cfg.Routes {
		for _, m := range r.Middleware {
			if m.Module == "cache" {
				inUse[cacheZone(m)] = true
			}
		}
	}
	live := make(map[string]cache.ZoneStats, len(zones))
	for _, z := range zones {
		live[z.Zone] = z
	}
	var diffs []Difference
	for _, name := range slices.Sorted(maps.Keys(inUse)) {
		if _, ok := live[name]; ok {
			continue
		}
		diffs = append(diffs, Difference{ID: "cache:zone:" + name + ":missing", Category: "cache", Kind: kindDrift, Name: name,
			Detail: fmt.Sprintf("a route uses cache zone %s, but no zone of that name is running", name),
			Config: name,
			Fixes:  []Fix{{Action: fixReapply, Label: "apply the config again to open the zone"}}})
	}
	for _, z := range zones {
		if inUse[z.Zone] {
			continue
		}
		diffs = append(diffs, Difference{ID: "cache:zone:" + z.Zone + ":kept", Category: "cache", Kind: kindInfo, Name: z.Zone,
			Detail:  fmt.Sprintf("cache zone %s is kept, though no route uses it now: a config applied before still holds it", z.Zone),
			Running: zoneState{Keys: z.Keys, Bytes: z.Bytes, DiskEntries: z.DiskEntries, DiskBytes: z.DiskBytes}})
	}
	return diffs
}

// cacheZone returns the zone a route's cache middleware uses: its zone
// param, or "default".
func cacheZone(m config.ModuleRef) string {
	var name string
	if v := m.Params.Field("zone"); !v.IsZero() {
		if err := v.Decode(&name); err != nil {
			return "default"
		}
	}
	if name == "" {
		return "default"
	}
	return name
}

// zoneState is a cache zone as a check reports it.
type zoneState struct {
	Keys        int   `json:"keys"`
	Bytes       int64 `json:"bytes"`
	DiskEntries int   `json:"disk_entries"`
	DiskBytes   int64 `json:"disk_bytes"`
}

// baseIndex returns the index of the element of the array member in the base
// config's tree whose name is name, or -1: an element a provider provided is
// not in it.
func baseIndex(base any, member, name string) int {
	o, ok := base.(*object)
	if !ok {
		return -1
	}
	v, ok := o.get(member)
	if !ok {
		return -1
	}
	arr, ok := v.([]any)
	if !ok {
		return -1
	}
	for i, e := range arr {
		if e, ok := e.(*object); ok {
			if n, ok := e.get("name"); ok {
				if s, ok := n.(string); ok && s == name {
					return i
				}
			}
		}
	}
	return -1
}

// acmeName is where a name is in a listener's tls.acme in the base config's
// tree.
type acmeName struct {
	listener, name int
}

// baseACMEName returns where name is in listener's tls.acme in the base
// config's tree, and whether it is written there.
func baseACMEName(base any, listener, name string) (acmeName, bool) {
	i := baseIndex(base, "listeners", listener)
	if i < 0 {
		return acmeName{}, false
	}
	o, ok := base.(*object)
	if !ok {
		return acmeName{}, false
	}
	v, _ := o.get("listeners")
	arr, ok := v.([]any)
	if !ok {
		return acmeName{}, false
	}
	lo, ok := arr[i].(*object)
	if !ok {
		return acmeName{}, false
	}
	tv, ok := lo.get("tls")
	if !ok {
		return acmeName{}, false
	}
	to, ok := tv.(*object)
	if !ok {
		return acmeName{}, false
	}
	av, ok := to.get("acme")
	if !ok {
		return acmeName{}, false
	}
	names, ok := av.([]any)
	if !ok {
		return acmeName{}, false
	}
	for j, v := range names {
		if s, ok := v.(string); ok && s == name {
			return acmeName{listener: i, name: j}, true
		}
	}
	return acmeName{}, false
}

// memberJSON returns v, a config member, as JSON, or nil when v is a nil
// pointer or a nil map or slice: a member the config leaves out.
func memberJSON(v any) any {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() {
		return nil
	}
	switch rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface, reflect.Func, reflect.Chan:
		if rv.IsNil() {
			return nil
		}
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	return json.RawMessage(data)
}

// memberSet reports whether member, as app.Info.Member returns it, is set: a
// nil pointer of the member's type is not, as app.Manager.Apply decides.
func memberSet(member any) bool {
	if member == nil {
		return false
	}
	v := reflect.ValueOf(member)
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Interface, reflect.Func, reflect.Chan:
		return !v.IsNil()
	}
	return true
}

// reconcileRequest is the body of POST /config/reconcile: the fixes a client
// selected, by the difference they belong to.
type reconcileRequest struct {
	Fixes []reconcileFix `json:"fixes"`
}

// reconcileFix is one selected fix: a difference's ID and the action the
// client chose from that difference's fixes.
type reconcileFix struct {
	ID     string `json:"id"`
	Action string `json:"action"`
}

// check answers GET /config/check: the differences between the running
// service and the config it runs from.
func (s *Server) check(w http.ResponseWriter, _ *http.Request) {
	res := s.configs.Check()
	w.Header().Set("ETag", res.ETag)
	w.Header().Set(VersionHeader, strconv.FormatUint(res.Version, 10))
	writeJSON(w, http.StatusOK, res)
}

// reconcile answers POST /config/reconcile: it checks again under the lock
// that applies the change, so that a selection is validated against the
// config it changes, applies the selected patches to the base config, and
// applies the result by the one path every config takes, as PUT /config
// does. Nothing is applied when a fix is no longer there, or the resulting
// config does not load or compile.
func (s *Server) reconcile(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("If-Match") == "" {
		writeError(w, http.StatusPreconditionRequired, "If-Match is needed: send the ETag GET /config/check answers with")
		return
	}
	data, ok := s.readBody(w, r, "application/json")
	if !ok {
		return
	}
	var req reconcileRequest
	if err := json.Unmarshal(data, &req); err != nil {
		writeError(w, http.StatusBadRequest, "the body: "+err.Error())
		return
	}
	if len(req.Fixes) == 0 {
		writeError(w, http.StatusBadRequest, "name at least one fix")
		return
	}
	res, err := s.configs.Update(s.source, r.Header.Get("If-Match"), func(cur *Version) ([]byte, error) {
		return s.configs.reconcile(cur, req.Fixes)
	})
	s.applied(w, res, err)
}

// reconcile returns the base config with the selected fixes applied, checked
// against the config it changes: cs.mu is held, as configs.Update's edit
// callback runs under it. A fix whose difference is gone, whose action its
// difference no longer offers, or whose patch its config no longer takes is
// a bad request.
func (cs *Configs) reconcile(cur *Version, fixes []reconcileFix) ([]byte, error) {
	diffs := cs.checkLocked(checkState{cur: cur, cfg: cs.currentCfg, first: cs.first, apps: cs.apps})
	byID := make(map[string]Difference, len(diffs))
	for _, d := range diffs {
		byID[d.ID] = d
	}
	selected := make([]Fix, 0, len(fixes))
	seen := make(map[string]bool, len(fixes))
	for _, f := range fixes {
		if seen[f.ID] {
			return nil, badRequest("the difference %q is named more than once", f.ID)
		}
		seen[f.ID] = true
		d, ok := byID[f.ID]
		if !ok {
			return nil, badRequest("the difference %q is not there any more: check again", f.ID)
		}
		i := slices.IndexFunc(d.Fixes, func(x Fix) bool { return x.Action == f.Action })
		if i < 0 {
			return nil, badRequest("the difference %q offers no %q fix", f.ID, f.Action)
		}
		selected = append(selected, d.Fixes[i])
	}
	if !slices.ContainsFunc(selected, func(f Fix) bool { return f.Patch != nil }) {
		return cur.Data(), nil
	}
	root, err := decodeJSON(cur.Data())
	if err != nil {
		return nil, err
	}
	var deletes []*ConfigPatch
	for _, f := range selected {
		if f.Patch == nil || f.Patch.Op == patchDelete {
			continue
		}
		if root, err = applyPatch(root, f.Patch); err != nil {
			return nil, err
		}
	}
	for _, f := range selected {
		if f.Patch != nil && f.Patch.Op == patchDelete {
			deletes = append(deletes, f.Patch)
		}
	}
	sortDeletes(deletes)
	for _, p := range deletes {
		if root, err = applyPatch(root, p); err != nil {
			return nil, err
		}
	}
	return encodeJSON(root), nil
}

// applyPatch applies p to root, a config's JSON tree, and returns the tree.
// A path the config no longer takes is a bad request.
func applyPatch(root any, p *ConfigPatch) (any, error) {
	root, err := patchTree(root, p)
	var re *RequestError
	if errors.As(err, &re) && re.Status == http.StatusNotFound {
		return nil, badRequest("%s", re.Msg)
	}
	return root, err
}

// patchTree is applyPatch without moving a not found path to a bad request.
func patchTree(root any, p *ConfigPatch) (any, error) {
	tokens, err := parsePointer(p.Path)
	if err != nil {
		return nil, err
	}
	switch p.Op {
	case patchAdd:
		if p.Value == nil {
			return nil, badRequest("the fix for %s adds nothing", p.Path)
		}
		v, err := decodeJSON(p.Value)
		if err != nil {
			return nil, err
		}
		if len(tokens) > 0 && tokens[len(tokens)-1] == "-" {
			parent := tokens[:len(tokens)-1]
			if _, err := lookup(root, parent); err != nil {
				if root, err = makeArray(root, parent); err != nil {
					return nil, err
				}
			}
		}
		return put(root, tokens, v)
	case patchReplace:
		if p.Value == nil {
			return nil, badRequest("the fix for %s replaces it with nothing", p.Path)
		}
		v, err := decodeJSON(p.Value)
		if err != nil {
			return nil, err
		}
		return put(root, tokens, v)
	case patchDelete:
		return remove(root, tokens)
	default:
		return nil, badRequest("the fix %q is not a patch operation", p.Op)
	}
}

// makeArray makes tokens point to an array in root, an empty one when the
// config leaves the member out, so that an element can be appended to it.
func makeArray(root any, tokens []string) (any, error) {
	cur := root
	for i, t := range tokens {
		o, ok := cur.(*object)
		if !ok {
			return nil, badRequest("%s is not an object", pointerString(tokens[:i]))
		}
		v, has := o.get(t)
		if i == len(tokens)-1 {
			if !has {
				o.set(t, []any{})
				return root, nil
			}
			if _, ok := v.([]any); !ok {
				return nil, badRequest("%s is not an array", pointerString(tokens))
			}
			return root, nil
		}
		if !has {
			v = &object{vals: map[string]any{}}
			o.set(t, v)
		}
		cur = v
	}
	return root, nil
}

// sortDeletes orders delete patches so that those of one array go from the
// highest index down, so that removing one does not move another.
func sortDeletes(patches []*ConfigPatch) {
	slices.SortStableFunc(patches, func(a, b *ConfigPatch) int {
		at, aerr := parsePointer(a.Path)
		bt, berr := parsePointer(b.Path)
		if aerr != nil || berr != nil || len(at) == 0 || len(bt) == 0 {
			return 0
		}
		for i := 0; i < min(len(at), len(bt))-1; i++ {
			if c := strings.Compare(at[i], bt[i]); c != 0 {
				return c
			}
		}
		if len(at) != len(bt) {
			return len(at) - len(bt)
		}
		ai, aok := strconv.Atoi(at[len(at)-1])
		bi, bok := strconv.Atoi(bt[len(bt)-1])
		if aok == nil && bok == nil {
			return bi - ai
		}
		return strings.Compare(at[len(at)-1], bt[len(bt)-1])
	})
}
