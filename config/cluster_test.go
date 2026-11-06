package config

import (
	"strings"
	"testing"
	"time"
)

func TestClusterDefaults(t *testing.T) {
	c := &Cluster{}
	if c.Interval() != 10*time.Second || c.CallTimeout() != 10*time.Second || c.Nodes() != DefaultClusterMaxNodes ||
		c.Concurrency() != DefaultClusterMaxConcurrency || c.ResponseBytes() != DefaultClusterMaxResponseBytes {
		t.Errorf("%+v", c)
	}
	c = &Cluster{PollInterval: Duration(time.Minute), Timeout: Duration(3 * time.Second), MaxNodes: 5, MaxConcurrency: 2, MaxResponseBytes: 1 << 20}
	if c.Interval() != time.Minute || c.CallTimeout() != 3*time.Second || c.Nodes() != 5 || c.Concurrency() != 2 || c.ResponseBytes() != 1<<20 {
		t.Errorf("%+v", c)
	}
}

func TestClusterErrors(t *testing.T) {
	const store = `"store": {"path": "/x", "password": "p"}`
	ok := `{"admin": {` + store + `, ` + assistantConsole + `, "cluster": {"poll_interval": "5s", "max_nodes": 10}}}`
	if _, err := Load([]byte(ok)); err != nil {
		t.Fatal(err)
	}
	// The store may be one fit run finds beside the config, which fit run
	// checks: the config alone does not need admin.store.
	if _, err := Load([]byte(`{"admin": {` + assistantConsole + `, "cluster": {}}}`)); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, doc string
		want      []string
	}{
		{"no console", `{"admin": {` + store + `, "cluster": {}}}`, []string{"admin.cluster: a manager's members are served in the console's workbench"}},
		{"bounds", `{"admin": {` + store + `, ` + assistantConsole + `, "cluster": {"poll_interval": "10ms", "timeout": "1ms", "max_response_bytes": 10}}}`,
			[]string{"poll_interval: 10ms is below 1s", "timeout: 1ms is below 1s", "max_response_bytes: 10 is below 16384"}},
		{"maximum", `{"admin": {` + store + `, ` + assistantConsole + `, "cluster": {"max_concurrency": 1000}}}`, []string{"max_concurrency: 1000 is greater than the maximum 256"}},
	} {
		_, err := Load([]byte(tc.doc))
		if err == nil {
			t.Errorf("%s: loaded", tc.name)
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(err.Error(), w) {
				t.Errorf("%s: %q lacks %q", tc.name, err, w)
			}
		}
	}
}
