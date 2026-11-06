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
	for _, tc := range []struct {
		name, doc string
		want      []string
	}{
		{"no store", `{"admin": {` + assistantConsole + `, "cluster": {}}}`, []string{"admin.cluster: a manager keeps its members' credentials in the store"}},
		{"no console", `{"admin": {` + store + `, "cluster": {}}}`, []string{"admin.cluster: a manager's members are served in the console's workbench"}},
		{"bounds", `{"admin": {` + store + `, ` + assistantConsole + `, "cluster": {"poll_interval": "10ms", "timeout": "1ms", "max_response_bytes": 10}}}`,
			[]string{"poll_interval: 10ms is below 1s", "timeout: 1ms is below 1s", "max_response_bytes: 10 is below 16384"}},
		{"maximum", `{"admin": {` + store + `, ` + assistantConsole + `, "cluster": {"max_concurrency": 1000}}}`, []string{"max_concurrency: 1000 is greater than the maximum 256"}},
		{"link", `{"admin": {` + store + `, ` + assistantConsole + `, "cluster": {"link_address": "7444", "link_url": "https://x"}}}`,
			[]string{"link_address: \"7444\" is not a host and a port", "link_url: \"https://x\" is not a wss:// URL"}},
		{"link url alone", `{"admin": {` + store + `, ` + assistantConsole + `, "cluster": {"link_url": "wss://m:7444/cluster/link"}}}`,
			[]string{"link_url: link_url names where members reach link_address: set link_address too"}},
		{"member", `{"admin": {"cluster_member": {"manager": "ws://m/x", "fingerprint": "ab", "token": "fite1.x"}}}`,
			[]string{"cluster_member.manager: \"ws://m/x\" is not a wss:// URL", "cluster_member.fingerprint: \"ab\" is not the SHA-256", "cluster_member.token: not an enrollment token"}},
		{"member token twice", `{"admin": {"cluster_member": {"manager": "wss://m:1/cluster/link", "fingerprint": "` + strings.Repeat("AB:", 31) + `AB", "token": "x", "token_file": "/t"}}}`,
			[]string{"cluster_member.token: token and token_file are two places for one token"}},
		{"member required", `{"admin": {"cluster_member": {}}}`, []string{"cluster_member.manager"}},
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

func TestClusterLinkSettings(t *testing.T) {
	ok := `{"admin": {"store": {"path": "/x", "password": "p"}, ` + assistantConsole + `, "cluster": {"link_address": ":7444", "link_url": "wss://m.example:7444/cluster/link"},
"cluster_member": {"manager": "wss://m.example:7444/cluster/link", "fingerprint": "` + strings.Repeat("0a", 32) + `", "token": "fite1.0123456789abcdef.` + strings.Repeat("A", 43) + `"}}}`
	c, err := Load([]byte(ok))
	if err != nil {
		t.Fatal(err)
	}
	if c.Admin.ClusterMember == nil || c.Admin.Cluster.LinkAddress != ":7444" {
		t.Fatalf("%+v", c.Admin)
	}
	if CheckClusterEnrollToken("fite1.0123456789abcdef."+strings.Repeat("A", 43)) != "" || CheckClusterEnrollToken("fitt1.0123456789abcdef."+strings.Repeat("A", 43)) == "" ||
		CheckClusterEnrollToken("fite1.0123456789abcdef."+strings.Repeat("!", 43)) == "" {
		t.Error("CheckClusterEnrollToken")
	}
	if !ValidFingerprint(strings.Repeat("Ab:", 31)+"cd") || ValidFingerprint(strings.Repeat("zz", 32)) || ValidFingerprint("ab") {
		t.Error("ValidFingerprint")
	}
	if CheckClusterLinkURL("wss://u@m:1/x") == "" || CheckClusterLinkURL("wss://m:1/x?y") == "" || CheckClusterLinkURL("wss://m:1"+ClusterLinkPath) != "" {
		t.Error("CheckClusterLinkURL")
	}
}
