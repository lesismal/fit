package admin

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Status is GET /status's answer: what fit is doing now, which the config
// does not say. The dashboard reads it.
type Status struct {
	Version     uint64             `json:"version"`
	Applied     time.Time          `json:"applied"`
	Source      string             `json:"source"`
	Reloads     reloadsStatus      `json:"reloads"`
	Listeners   []listenerJSON     `json:"listeners"`
	Connections connectionsStatus  `json:"connections"`
	Upstreams   []upstreamStatus   `json:"upstreams"`
	Certs       []certificateState `json:"certificates"`
}

type reloadsStatus struct {
	Succeeded uint64 `json:"succeeded"`
	Failed    uint64 `json:"failed"`
}

type connectionsStatus struct {
	Open         int64  `json:"open"`
	Refused      uint64 `json:"refused"`
	PendingBytes int64  `json:"pending_bytes"`
}

type upstreamStatus struct {
	Name     string          `json:"name"`
	InFlight int64           `json:"in_flight"`
	Backends []backendStatus `json:"backends"`
}

type backendStatus struct {
	Address string `json:"address"`
	// Up says the backend is in rotation; ProbedDown that its health
	// check, not failed requests, took it out.
	Up         bool   `json:"up"`
	ProbedDown bool   `json:"probed_down,omitempty"`
	InFlight   int64  `json:"in_flight"`
	Succeeded  uint64 `json:"succeeded"`
	Failed     uint64 `json:"failed"`
}

type certificateState struct {
	Listener  string    `json:"listener"`
	Names     []string  `json:"names"`
	Issuer    string    `json:"issuer"`
	Serial    string    `json:"serial"`
	NotBefore time.Time `json:"not_before"`
	NotAfter  time.Time `json:"not_after"`
}

// Status returns what fit is doing now: the running config's version, its
// listeners and connections, its upstreams and their backends' health,
// and the certificates its listeners serve.
func (cs *Configs) Status() Status {
	cur := cs.Current()
	tried, failed := cs.Reloads()
	st := Status{Version: cur.Number, Applied: cur.Applied, Source: cur.Source,
		Reloads: reloadsStatus{Succeeded: tried - failed, Failed: failed}}
	st.Listeners = listeners(cs.server.Addrs())
	ss := cs.server.Stats()
	st.Connections = connectionsStatus{Open: ss.OpenConns, Refused: ss.ConnsRefused, PendingBytes: ss.PendingBytes}
	st.Upstreams, st.Certs = []upstreamStatus{}, []certificateState{}
	if g := cs.rt.Current(); g != nil {
		defer g.Release()
		for _, p := range g.Upstreams() {
			u := upstreamStatus{Name: p.Name(), InFlight: p.InFlight(), Backends: []backendStatus{}}
			for _, b := range p.Backends() {
				done, failed := b.Requests()
				u.Backends = append(u.Backends, backendStatus{Address: b.Address, Up: !b.Down(), ProbedDown: b.ProbedDown(),
					InFlight: b.Active(), Succeeded: done - failed, Failed: failed})
			}
			st.Upstreams = append(st.Upstreams, u)
		}
		slices.SortFunc(st.Upstreams, func(a, b upstreamStatus) int { return strings.Compare(a.Name, b.Name) })
		for _, l := range g.Listeners {
			lister, ok := l.Certificates.(certificateLister)
			if !ok {
				continue
			}
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
				st.Certs = append(st.Certs, certificateState{Listener: l.Name, Names: names, Issuer: c.Leaf.Issuer.CommonName,
					Serial: c.Leaf.SerialNumber.Text(16), NotBefore: c.Leaf.NotBefore, NotAfter: c.Leaf.NotAfter})
			}
		}
		slices.SortStableFunc(st.Certs, func(a, b certificateState) int {
			if c := strings.Compare(a.Listener, b.Listener); c != 0 {
				return c
			}
			return a.NotAfter.Compare(b.NotAfter)
		})
	}
	return st
}

func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	st := s.configs.Status()
	w.Header().Set(VersionHeader, strconv.FormatUint(st.Version, 10))
	writeJSON(w, http.StatusOK, st)
}
