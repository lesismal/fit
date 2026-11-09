package admin

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/observe"
	"github.com/lesismal/fit/runtime"
)

// TestBTSummary: GET /bittorrent/summary answers 200 with neither role
// running, and with both sums the client's torrents by state, gives its
// address and its publish entries, and the tracker's statistics; /node
// lists its capability.
func TestBTSummary(t *testing.T) {
	bare := &btFixture{fixture: startEnv(t, doc("x"), &config.Admin{}, runtime.Env{Metrics: observe.NewMetrics(), Engine: testEngine(t)})}
	resp, body := bare.get(t, "/bittorrent/summary")
	if resp.StatusCode != http.StatusOK || strings.TrimSpace(body) != `{"client":null,"tracker":null}` {
		t.Fatalf("without the roles: %d %s", resp.StatusCode, body)
	}
	if _, body := bare.get(t, "/node"); !strings.Contains(body, `"`+CapBTSummary+`"`) {
		t.Errorf("/node: %s", body)
	}

	f := startBT(t, &config.Admin{}, "")
	films := filepath.Join(f.dl, "films")
	raw := btTorrentFile(t, films, "a.bin", 100000)
	f.json(t, "POST", "/bittorrent/torrents", `{"torrent": "`+base64.StdEncoding.EncodeToString(raw)+`", "category": "films", "paused": true}`, http.StatusCreated)
	_, body = f.get(t, "/bittorrent/summary")
	var s BTSummary
	if err := json.Unmarshal([]byte(body), &s); err != nil {
		t.Fatal(err)
	}
	c := s.Client
	if c == nil || !c.Running || c.Listen != f.role.Addr().String() || c.Torrents != 1 || c.States["paused"]+c.States["checking"] != 1 ||
		c.Size != 100000 || c.Publish == nil || s.Tracker == nil || s.Tracker.MaxTorrents == 0 {
		t.Fatalf("%s", body)
	}
}
