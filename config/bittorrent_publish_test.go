package config

import (
	"strings"
	"testing"
	"time"
)

func TestBitTorrentPublish(t *testing.T) {
	head := `{"bittorrent": {"client": {"listen": ":6881", "data_dir": "/var/lib/bt", "publish": `
	c, err := Load([]byte(head + `[{"name": "isos", "dir": "/srv/isos", "web_seed": "https://example.com/isos/",
		"announce": ["https://example.com/announce", "udp://example.com:6969"], "private": true, "piece_length": 262144,
		"files": ["*.iso"], "exclude": ["**/tmp/**"], "check_interval": "1m", "max_files": 10, "max_total_bytes": 1000,
		"max_hashing": 2}, {"name": "all", "dir": "/srv/all", "mode": "whole_dir", "web_seed": "http://example.com/x/all"}]}}}`))
	if err != nil {
		t.Fatal(err)
	}
	p := c.BitTorrent.Client.Publish
	if len(p) != 2 || p[0].PublishMode() != BTPublishFiles || p[0].Interval() != time.Minute || p[0].Files() != 10 ||
		p[0].TotalBytes() != 1000 || p[0].Hashing() != 2 || p[1].PublishMode() != BTPublishDir {
		t.Fatalf("%+v", p)
	}
	d := &BitTorrentPublish{}
	if d.Interval() != DefaultBTPublishCheckInterval || d.Files() != DefaultBTPublishMaxFiles ||
		d.TotalBytes() != DefaultBTPublishMaxTotalBytes || d.Hashing() != DefaultBTPublishMaxHashing {
		t.Errorf("defaults %+v", d)
	}
	for _, bad := range []struct{ doc, want string }{
		{`[{"name": "-x", "dir": "/a"}]`, "not a valid name"},
		{`[{"name": "a", "dir": "/a"}, {"name": "a", "dir": "/b"}]`, `another publish entry is already named "a"`},
		{`[{"name": "a", "dir": " "}]`, "dir may not be empty"},
		{`[{"name": "a", "dir": "/"}]`, "root of a file system"},
		{`[{"name": "a"}]`, "dir"},
		{`[{"name": "a", "dir": "/a", "mode": "each"}]`, "mode"},
		{`[{"name": "a", "dir": "/a", "web_seed": "ftp://x/a/"}]`, "not an http or https URL"},
		{`[{"name": "a", "dir": "/a", "web_seed": "http://x/a/?k=v"}]`, "query or a fragment"},
		{`[{"name": "a", "dir": "/a", "mode": "whole_dir", "web_seed": "http://x/b/"}]`, `last element must be the directory's, "a"`},
		{`[{"name": "a", "dir": "/a", "announce": ["ws://x/announce"]}]`, "not an http, https or udp tracker URL"},
		{`[{"name": "a", "dir": "/a", "piece_length": 30000}]`, "not a power of two"},
		{`[{"name": "a", "dir": "/a", "piece_length": 8192}]`, "not a power of two"},
		{`[{"name": "a", "dir": "/a", "piece_length": 134217728}]`, "greater than the maximum"},
		{`[{"name": "a", "dir": "/a", "files": ["["]}]`, "is not a pattern"},
		{`[{"name": "a", "dir": "/a", "exclude": ["/abs"]}]`, "is not a pattern"},
		{`[{"name": "a", "dir": "/a", "check_interval": "-1s"}]`, "is negative"},
		{`[{"name": "a", "dir": "/a", "max_files": 200000}]`, "greater than the maximum"},
		{`[{"name": "a", "dir": "/a", "max_hashing": 17}]`, "greater than the maximum"},
	} {
		_, err := Load([]byte(head + bad.doc + `}}}`))
		if err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%s: %v, want %q", bad.doc, err, bad.want)
		}
	}
	var many strings.Builder
	many.WriteString("[")
	for i := range MaxBTPublish + 1 {
		if i > 0 {
			many.WriteString(",")
		}
		many.WriteString(`{"name": "e` + string(rune('a'+i%26)) + string(rune('a'+i/26)) + `", "dir": "/a"}`)
	}
	many.WriteString("]")
	if _, err := Load([]byte(head + many.String() + `}}}`)); err == nil || !strings.Contains(err.Error(), "publish entries, more than") {
		t.Errorf("%d entries: %v", MaxBTPublish+1, err)
	}
}
