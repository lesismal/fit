package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/lesismal/fit/internal/managed"
)

// CapManaged: GET, PUT and DELETE /node/managed, what a manager of many fits
// owns in this fit's config (I145).
const CapManaged = "node.managed"

// Where the parts a manager owns are kept (managedBody.Kept): in the store's
// section managed.Section, or, without a store, in memory until fit stops,
// the manager telling it again when it next compares.
const (
	ManagedInStore  = "store"
	ManagedInMemory = "memory"
)

// managedState is the record a manager gave this fit, kept in memory when
// there is no store.
type managedState struct {
	// wmu orders the changes, from whichever server; mu guards rec.
	wmu sync.Mutex
	mu  sync.Mutex
	rec *managed.Record
}

// managedPath is a part as GET /node/managed answers it: the record's,
// with whether the running config has the part and whether it has changed
// since the manager saw it in sync.
type managedPath struct {
	managed.Path
	Display string `json:"path"`
	Present bool   `json:"present"`
	// Changed is a part whose value is no longer the one the manager last
	// saw: changed or removed here since. The manager shows it as a
	// conflict.
	Changed bool `json:"changed"`
}

// managedBody is GET and PUT /node/managed's answer.
type managedBody struct {
	Manager     string        `json:"manager"`
	ManagerHost string        `json:"manager_host,omitempty"`
	Updated     any           `json:"updated,omitempty"`
	Kept        string        `json:"kept"`
	Paths       []managedPath `json:"paths"`
}

// Managed returns the record of what a manager owns here, and where it is
// kept: an empty record when no manager said anything.
func (cs *Configs) Managed() (managed.Record, string) {
	if st := cs.Sealer(); st != nil {
		var rec managed.Record
		if data := st.Section(managed.Section); data != nil && json.Unmarshal(data, &rec) == nil && rec.Check() == nil {
			rec.Normalize()
			return rec, ManagedInStore
		}
		return managed.Record{Paths: []managed.Path{}}, ManagedInStore
	}
	cs.managed.mu.Lock()
	defer cs.managed.mu.Unlock()
	if cs.managed.rec == nil {
		return managed.Record{Paths: []managed.Path{}}, ManagedInMemory
	}
	rec := *cs.managed.rec
	rec.Paths = append([]managed.Path{}, rec.Paths...)
	return rec, ManagedInMemory
}

// SetManaged keeps rec, checked, as what a manager owns here: in the store
// when there is one. It writes a file: off fib's workers.
func (cs *Configs) SetManaged(rec managed.Record) error {
	if err := rec.Check(); err != nil {
		return err
	}
	rec.Normalize()
	if st := cs.Sealer(); st != nil {
		data, _ := json.Marshal(rec) // strings and a time
		return st.SetSection(managed.Section, data)
	}
	cs.managed.mu.Lock()
	defer cs.managed.mu.Unlock()
	cs.managed.rec = &rec
	return nil
}

// managedView is what GET /node/managed answers of rec, against the running
// config.
func (cs *Configs) managedView(rec *managed.Record, kept string) managedBody {
	body := managedBody{Manager: rec.Manager, ManagerHost: rec.ManagerHost, Kept: kept, Paths: []managedPath{}}
	if !rec.Updated.IsZero() {
		body.Updated = rec.Updated.UTC()
	}
	var top map[string]json.RawMessage
	_ = json.Unmarshal(cs.Current().Data(), &top) // the running config is an object
	for _, p := range rec.Paths {
		v, ok, _ := managed.LookupIn(top, p.Member, p.Name)
		mp := managedPath{Path: p, Display: p.String(), Present: ok}
		mp.Changed = p.Hash != "" && (!ok || managed.Hash(v) != p.Hash)
		body.Paths = append(body.Paths, mp)
	}
	return body
}

// getManaged answers GET /node/managed: the parts of this fit's config a
// manager of many fits owns, each with whether it changed here since the
// manager saw it, with the record's ETag.
func (s *Server) getManaged(w http.ResponseWriter, r *http.Request) {
	rec, kept := s.configs.Managed()
	w.Header().Set("ETag", rec.ETag())
	writeJSON(w, http.StatusOK, s.configs.managedView(&rec, kept))
}

// putManaged answers PUT /node/managed: a manager says what it owns here,
// {"manager", "manager_host", "paths": [{"member", "name", "template",
// "hash"}]}, with the record's ETag as If-Match when it read it. Nothing in
// the config changes: the pages mark those parts, and ask before a change
// to them.
func (s *Server) putManaged(w http.ResponseWriter, r *http.Request) {
	data, ok := s.readBody(w, r, "application/json")
	if !ok {
		return
	}
	if len(data) > managed.MaxRecordBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "a record of what a manager owns is 1 MiB at most")
		return
	}
	var rec managed.Record
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&rec); err != nil {
		writeError(w, http.StatusBadRequest, "the body is not a record of what a manager owns: "+err.Error())
		return
	}
	s.setManaged(w, r, rec)
}

// deleteManaged answers DELETE /node/managed: no manager owns anything here
// any more.
func (s *Server) deleteManaged(w http.ResponseWriter, r *http.Request) {
	s.setManaged(w, r, managed.Record{Paths: []managed.Path{}})
}

func (s *Server) setManaged(w http.ResponseWriter, r *http.Request, rec managed.Record) {
	s.configs.managed.wmu.Lock()
	defer s.configs.managed.wmu.Unlock()
	old, _ := s.configs.Managed()
	if im := r.Header.Get("If-Match"); im != "" && im != "*" && im != old.ETag() {
		writeError(w, http.StatusPreconditionFailed, "what a manager owns here changed since the ETag given in If-Match")
		return
	}
	if err := rec.Check(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.configs.SetManaged(rec); err != nil {
		writeError(w, http.StatusInternalServerError, "keeping what a manager owns: "+err.Error())
		return
	}
	rec, kept := s.configs.Managed()
	w.Header().Set("ETag", rec.ETag())
	writeJSON(w, http.StatusOK, s.configs.managedView(&rec, kept))
}
