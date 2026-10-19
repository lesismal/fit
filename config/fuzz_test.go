package config

import (
	"encoding/json"
	"testing"
)

// FuzzLoad checks that Load never panics, and that a config it accepts
// survives being written back out and loaded again.
func FuzzLoad(f *testing.F) {
	f.Add([]byte(validConfig))
	f.Add([]byte(`{"listeners": [{"name": "a", "address": ":1"}]}`))
	f.Add([]byte(`{"listeners": [], "listeners": []}`))
	f.Add([]byte(`[[[[{"a": "é"}]]]]`))
	f.Fuzz(func(t *testing.T, data []byte) {
		c, err := Load(data)
		if err != nil {
			for _, e := range AsErrors(err) {
				if e.Msg == "" {
					t.Fatalf("an error with no message: %+v", e)
				}
			}
			return
		}
		out, err := json.Marshal(c)
		if err != nil {
			t.Fatalf("marshalling a loaded config: %v", err)
		}
		if _, err := Load(out); err != nil {
			t.Fatalf("reloading %s: %v", out, err)
		}
	})
}
