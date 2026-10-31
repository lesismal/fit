package admin

import (
	"bytes"
	"errors"

	"github.com/lesismal/fit/config"
)

// SourceStore is the config read from the store, as SIGHUP reads it again.
const SourceStore = "store"

// SplitAdmin splits a config, standard JSON, into its admin, as JSON, nil
// when it has none, and the rest, written out again without it.
func SplitAdmin(data []byte) (admin, rest []byte, err error) {
	root, err := decodeJSON(data)
	if err != nil {
		return nil, nil, err
	}
	o, ok := root.(*object)
	if !ok {
		return nil, nil, errors.New("the config is not a JSON object")
	}
	a, ok := o.get("admin")
	if !ok {
		return nil, data, nil
	}
	o.del("admin")
	return encodeJSON(a), encodeJSON(o), nil
}

// WithAdmin returns data, a config as a JSON object, with admin added as its
// last member, after a line break, so that every position in data stays
// where it was and errors in it are reported where they were written.
func WithAdmin(data, admin []byte) []byte {
	end := bytes.LastIndexByte(data, '}')
	if end < 0 {
		return data // not an object: loading it says so
	}
	sep := ",\n"
	if len(bytes.TrimSpace(data[bytes.IndexByte(data, '{')+1:end])) == 0 {
		sep = "\n"
	}
	out := make([]byte, 0, len(data)+len(admin)+16)
	out = append(out, data[:end]...)
	out = append(out, sep+`"admin": `...)
	out = append(out, admin...)
	return append(out, "\n}"...)
}

// hasAdmin reports whether a config, standard JSON, sets admin.
func hasAdmin(data []byte) bool {
	root, err := decodeJSON(data)
	if err != nil {
		return false // loading it reports what is wrong
	}
	o, ok := root.(*object)
	if !ok {
		return false
	}
	_, ok = o.get("admin")
	return ok
}

// errAdminInStore is a config with admin of its own, while admin comes from
// the bootstrap file.
var errAdminInStore = config.Errors{{Path: "admin", Msg: "admin is set in the config file fit started with, while the rest of the config is kept in the store; it changes when fit restarts"}}
