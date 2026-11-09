package admin

import (
	"net/http"

	"github.com/lesismal/fit/bittorrent"
	"github.com/lesismal/fit/bittorrent/tracker"
)

// GET /bittorrent/summary (I206): the BitTorrent roles of this fit in one
// small answer, for a manager's view of its members' torrents. It answers
// 200 whether the roles run or not, so that the manager tells a member
// without them from one it cannot read:
//
//	{"client": null | {"running", "listen", "torrents", "states",
//	  "download_rate", "upload_rate", "downloaded", "uploaded", "size",
//	  "completed", "peers", "errors", "publish": [{"name", "torrents",
//	  "pending", "error"}]},
//	 "tracker": null | the tracker's stats}
//
// listen is the address the client takes peers on, TCP and UDP, which the
// manager gives the members a distribution adds a torrent on as their
// peers. The publish entries are the first maxSummaryPublish by name.

// CapBTSummary: GET /bittorrent/summary.
const CapBTSummary = "bittorrent.summary"

// maxSummaryPublish bounds the publish entries the summary lists: the
// config allows 64.
const maxSummaryPublish = 64

// BTClientSummary is the client role's part of the summary.
type BTClientSummary struct {
	// Running says the role runs a client now.
	Running bool   `json:"running"`
	Listen  string `json:"listen,omitempty"`
	// Torrents counts the torrents of the role's list, States them by
	// state (bittorrent.States).
	Torrents int            `json:"torrents"`
	States   map[string]int `json:"states"`
	// The sums over the torrents listed: rates in bytes a second, bytes
	// downloaded and uploaded since each was added to the running client,
	// their sizes and the bytes they have, the peers connected, and how
	// many have an error.
	DownloadRate float64 `json:"download_rate"`
	UploadRate   float64 `json:"upload_rate"`
	Downloaded   int64   `json:"downloaded"`
	Uploaded     int64   `json:"uploaded"`
	Size         int64   `json:"size"`
	Completed    int64   `json:"completed"`
	Peers        int     `json:"peers"`
	Errors       int     `json:"errors"`
	// Publish are the publish entries, each with its torrents' count.
	Publish []BTPublishSummary `json:"publish"`
}

// BTPublishSummary is a publish entry in the summary.
type BTPublishSummary struct {
	Name     string `json:"name"`
	Torrents int    `json:"torrents"`
	Pending  int    `json:"pending"`
	Error    string `json:"error,omitempty"`
}

// BTSummary is GET /bittorrent/summary's answer.
type BTSummary struct {
	Client  *BTClientSummary `json:"client"`
	Tracker *tracker.Stats   `json:"tracker"`
}

// btSummary answers GET /bittorrent/summary.
func (s *Server) btSummary(w http.ResponseWriter, _ *http.Request) {
	var out BTSummary
	if role := bittorrent.Lookup(s.configs.env.Engine); role != nil {
		out.Client = summarizeClient(role)
	}
	if t := tracker.Lookup(s.configs.env.Engine); t != nil {
		if st, err := t.Stats(); err == nil {
			out.Tracker = &st
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// summarizeClient sums the role's torrents.
func summarizeClient(role *bittorrent.Role) *BTClientSummary {
	c := &BTClientSummary{Running: role.ClientRunning(), States: map[string]int{}, Publish: []BTPublishSummary{}}
	if a := role.Addr(); a.IsValid() {
		c.Listen = a.String()
	}
	for _, st := range role.List() {
		c.Torrents++
		c.States[st.State]++
		c.DownloadRate += st.DownloadRate
		c.UploadRate += st.UploadRate
		c.Downloaded += st.Downloaded
		c.Uploaded += st.Uploaded
		c.Size += st.Size
		c.Completed += st.Completed
		c.Peers += st.Peers
		if st.Error != "" || st.State == "error" {
			c.Errors++
		}
	}
	for _, p := range role.Publishing(nil) {
		if len(c.Publish) == maxSummaryPublish {
			break
		}
		c.Publish = append(c.Publish, BTPublishSummary{Name: p.Name, Torrents: len(p.Torrents), Pending: p.Pending, Error: p.Error})
	}
	return c
}
