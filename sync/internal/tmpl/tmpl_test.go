package tmpl

import (
	"testing"

	fitsync "github.com/lesismal/fit/sync"
)

func TestTemplates(t *testing.T) {
	r := &fitsync.Record{Namespace: "db.t"}
	row := fitsync.Map(fitsync.Field{Name: "dev", Value: fitsync.Int(7)}, fitsync.Field{Name: "none", Value: fitsync.Null()})
	for tmpl, want := range map[string]string{"{namespace}": "db.t", "m_{dev}_x": "m_7_x", "{{a}": "{a}", "plain": "plain", "": ""} {
		parts, err := Parse(tmpl)
		if err != nil {
			t.Fatal(tmpl, err)
		}
		got, err := Expand(parts, r, row)
		if err != nil || got != want {
			t.Errorf("%s: %q %v", tmpl, got, err)
		}
		s, static := Static(parts)
		if static != (tmpl == "plain" || tmpl == "{{a}" || tmpl == "") || static && s != want {
			t.Errorf("%s: static %q %v", tmpl, s, static)
		}
	}
	for _, bad := range []string{"{", "a{}b"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
	for _, missing := range []string{"{other}", "{none}"} {
		parts, _ := Parse(missing)
		if _, err := Expand(parts, r, row); err == nil {
			t.Errorf("%s expanded", missing)
		}
	}
}
