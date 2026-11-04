package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/lesismal/fit/adapter"
	"github.com/lesismal/fit/adapter/fitconf"
	"github.com/lesismal/fit/adapter/yaml"
	"github.com/lesismal/fit/admin/store"
)

// StorePasswordHeader carries the password of a fitstore file imported
// with POST /config/import?format=store, when it was sealed with one of its
// own rather than the running store's.
const StorePasswordHeader = "Fit-Store-Password"

// formatStore is the fitstore format of an export or an import, beside the
// text formats.
const formatStore = "store"

// exportBody is POST /config/export's request.
type exportBody struct {
	Format   string   `json:"format"`
	Members  []string `json:"members,omitempty"`
	Password string   `json:"password,omitempty"`
}

// exportConfig answers the running base config, or the top-level members of
// it a page edits, as a file in one of the text formats or as a fitstore
// file: POST /config/export. admin is never exported. A fitstore file is
// sealed with the password the request gives, or with the running store's
// key when it gives none; a whole config's holds the console users the store
// keeps beside it too, so that a restore logs in with the changed passwords.
func (s *Server) exportConfig(w http.ResponseWriter, r *http.Request) {
	data, ok := s.readBody(w, r, "application/json")
	if !ok {
		return
	}
	var body exportBody
	if err := json.Unmarshal(data, &body); err != nil {
		writeError(w, http.StatusBadRequest, `the body must be {"format", "members", "password"}`)
		return
	}
	members, err := cleanMembers(body.Members)
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	v := s.configs.Current()
	root, err := decodeObject(v.Data())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "the running config is not a JSON object")
		return
	}
	root.del("admin")
	if len(members) > 0 {
		part := &object{vals: map[string]any{}}
		for _, m := range members {
			if val, ok := root.get(m); ok {
				part.set(m, val)
			}
		}
		root = part
	}
	std := encodeJSON(root)
	var out []byte
	contentType := "text/plain; charset=utf-8"
	switch body.Format {
	case "conf":
		out, err = fitconf.FromJSON(std)
	case "yaml":
		out, err = yaml.FromJSON(std)
	case "json":
		var b bytes.Buffer
		if err = json.Indent(&b, bytes.TrimSpace(std), "", "  "); err == nil {
			b.WriteByte('\n')
			out = b.Bytes()
		}
	case formatStore:
		contentType = "application/octet-stream"
		var users []store.User
		if len(members) == 0 {
			users, _ = s.configs.ConsoleUsers()
		}
		if body.Password != "" {
			out, err = store.Seal(body.Password, std, users)
		} else if st := s.configs.Sealer(); st != nil {
			out, err = st.Seal(std, users)
		} else {
			writeError(w, http.StatusBadRequest, "fit runs without a store: give the password to seal the export with")
			return
		}
	default:
		writeError(w, http.StatusBadRequest, "the format must be conf, yaml, json or store")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "exporting the config: "+err.Error())
		return
	}
	name := "config"
	if len(members) > 0 {
		name = strings.Join(members, "-")
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="fit-%s-v%d.%s"`, name, v.Number, body.Format))
	w.Header().Set("ETag", v.ETag)
	w.Header().Set(VersionHeader, strconv.FormatUint(v.Number, 10))
	writeText(w, contentType, out)
}

// importConfig applies a file in one of the text formats or a fitstore
// file, by the path every config takes: POST /config/import?format=...
// &members=.... With members, those top-level members are taken from the
// file, the others it sets being left out, and a member it lacks is removed
// from the config; without, the file replaces the whole config. admin is
// never imported: the running config's stays, and a file's is left out. A
// fitstore file opens with the password in Fit-Store-Password, or the
// running store's when there is none; the console users it may hold are not
// imported.
func (s *Server) importConfig(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("format")
	members, err := queryMembers(r)
	if err != nil {
		writeUpdateError(w, err)
		return
	}
	var adapted *adapter.Result
	var fragJSON []byte
	if name == formatStore {
		data, ok := s.readBody(w, r, "application/octet-stream")
		if !ok {
			return
		}
		password := r.Header.Get(StorePasswordHeader)
		switch {
		case password != "":
			fragJSON, _, err = store.Unseal(data, password)
		case s.configs.Sealer() != nil:
			fragJSON, _, err = s.configs.Sealer().Unseal(data)
		default:
			writeError(w, http.StatusBadRequest, "fit runs without a store: give the file's password in "+StorePasswordHeader)
			return
		}
		if err != nil {
			msg := err.Error()
			if errors.Is(err, store.ErrOpen) {
				msg = "the file does not open with the password: it is wrong, or the file was changed"
			}
			writeError(w, http.StatusBadRequest, msg)
			return
		}
	} else {
		f, ok := s.textFormatOrBadRequest(w, r)
		if !ok {
			return
		}
		data, ok := s.readBody(w, r, f.types...)
		if !ok {
			return
		}
		if adapted, err = adaptText(f, data); err != nil {
			writeTextParseError(w, err)
			return
		}
		fragJSON = adapted.JSON
	}
	frag, err := decodeObject(fragJSON)
	if err != nil {
		writeError(w, http.StatusBadRequest, "the file is not a config object")
		return
	}
	res, err := s.configs.Update(s.source, r.Header.Get("If-Match"), func(cur *Version) ([]byte, error) {
		base, err := decodeObject(cur.Data())
		if err != nil {
			return nil, badRequest("the running config is not a JSON object")
		}
		take := members
		if len(take) == 0 {
			// The whole config: every member but admin is the file's.
			for _, k := range slices.Clone(base.keys) {
				if k != "admin" {
					base.del(k)
				}
			}
			for _, k := range frag.keys {
				if k != "admin" {
					take = append(take, k)
				}
			}
		}
		for _, m := range take {
			if v, ok := frag.get(m); ok {
				base.set(m, v)
			} else {
				base.del(m)
			}
		}
		return encodeJSON(base), nil
	})
	if err != nil {
		if adapted != nil {
			s.writeTextUpdateError(w, adapted, err)
		} else {
			s.applied(w, res, err)
		}
		return
	}
	s.applied(w, res, nil)
}

// cleanMembers checks a list of top-level member names as queryMembers
// does, and returns it without duplicates.
func cleanMembers(names []string) ([]string, error) {
	var out []string
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if name == "admin" {
			return nil, badRequest("admin is not exported: it comes from the config fit started with")
		}
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	return out, nil
}
