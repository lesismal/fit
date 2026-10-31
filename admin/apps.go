package admin

import (
	"github.com/lesismal/fit/app"
)

// UseApps has every config applied from here on move apps' roles to it,
// after its listeners: a role that fails to keeps running as it did, and
// its error is one of the config's warnings.
func (cs *Configs) UseApps(apps *app.Manager) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	cs.apps = apps
}

// EditMember applies, as source, the running base config with its member
// at path changed by edit, which is given the member as it is, as JSON, or
// nil when the base config leaves it out, and returns it changed; the
// members on the way to it are made when missing. It returns the warnings
// applying it gave, with one more when the config is not kept in a store,
// so that the change is lost when fit restarts or reads its files again.
// It is an app.Editor.
func (cs *Configs) EditMember(source string, path []string, edit func(cur []byte) ([]byte, error)) ([]string, error) {
	r, err := cs.Update(source, "", func(cur *Version) ([]byte, error) {
		root, err := decodeJSON(cur.data)
		if err != nil {
			return nil, err
		}
		if _, ok := root.(*object); !ok {
			return nil, badRequest("the config is not a JSON object")
		}
		var was []byte
		parent := root
		for i, t := range path {
			o, ok := parent.(*object)
			if !ok {
				return nil, badRequest("%s is not an object", pointerString(path[:i]))
			}
			v, ok := o.get(t)
			if !ok {
				if i == len(path)-1 {
					break
				}
				v = &object{vals: map[string]any{}}
				o.set(t, v)
			}
			if i == len(path)-1 {
				was = encodeJSON(v)
			}
			parent = v
		}
		data, err := edit(was)
		if err != nil {
			return nil, err
		}
		v, err := decodeJSON(data)
		if err != nil {
			return nil, err
		}
		if root, err = put(root, path, v); err != nil {
			return nil, err
		}
		return encodeJSON(root), nil
	})
	if err != nil {
		return nil, err
	}
	cs.mu.Lock()
	kept := cs.save != nil
	cs.mu.Unlock()
	if !kept {
		r.Warnings = append(r.Warnings, pointerString(path)+" changed in the running config only: without admin.store, the change is lost when fit restarts or reads its files again")
	}
	return r.Warnings, nil
}

var _ app.Editor = (*Configs)(nil).EditMember
