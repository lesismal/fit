package bittorrent

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// The RSS state (I208), data_dir/rss.json: for each feed its ETag and
// Last-Modified, its last poll, the API's toggle and the items seen, the
// oldest first, at most history_size; for each rule the API's toggle, the
// episodes it added, at most MaxRSSEpisodes, and how many torrents it added.
// Written as the torrents' state is, to a temporary file renamed into
// place, after every poll that changed it and when the role stops.

// RSSStateVersion is the version of rss.json's format.
const RSSStateVersion = 1

const (
	rssStateFile = "rss.json"
	// MaxRSSStateBytes bounds rss.json when read back.
	MaxRSSStateBytes = 64 << 20
	// MaxRSSEpisodes bounds the episodes a rule remembers, the oldest
	// forgotten first.
	MaxRSSEpisodes = 10000
)

// rssDoc is rss.json.
type rssDoc struct {
	Version int                    `json:"version"`
	Saved   time.Time              `json:"saved"`
	Feeds   map[string]*rssFeedDoc `json:"feeds,omitempty"`
	Rules   map[string]*rssRuleDoc `json:"rules,omitempty"`
}

// rssFeedDoc is a feed in rss.json.
type rssFeedDoc struct {
	ETag         string    `json:"etag,omitempty"`
	LastModified string    `json:"last_modified,omitempty"`
	Enabled      *bool     `json:"enabled,omitempty"`
	Title        string    `json:"title,omitempty"`
	LastPoll     time.Time `json:"last_poll,omitzero"`
	LastResult   string    `json:"last_result,omitempty"`
	LastError    string    `json:"last_error,omitempty"`
	Items        []RSSItem `json:"items,omitempty"`
}

// rssRuleDoc is a rule in rss.json.
type rssRuleDoc struct {
	Enabled   *bool     `json:"enabled,omitempty"`
	Episodes  []string  `json:"episodes,omitempty"`
	Added     int64     `json:"added,omitempty"`
	LastAdded time.Time `json:"last_added,omitzero"`
}

// loadRSSState reads dir's rss.json; none is an empty state. One that does
// not read is moved aside, as rss.json.bad, and an empty state begins: what
// it remembered is only which items were seen, and a torrent added again
// is refused as held already.
func loadRSSState(dir string) (*rssDoc, error) {
	p := filepath.Join(dir, rssStateFile)
	data, err := readBounded(p, MaxRSSStateBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return &rssDoc{}, nil
	}
	var doc rssDoc
	if err == nil {
		err = json.Unmarshal(data, &doc)
		if err == nil && doc.Version > RSSStateVersion {
			err = fmt.Errorf("version %d, later than %d", doc.Version, RSSStateVersion)
		}
	}
	if err != nil {
		bad := p + ".bad"
		if rerr := os.Rename(p, bad); rerr != nil {
			return &rssDoc{}, fmt.Errorf("%s does not read (%v), nor can it be moved aside: %v", p, err, rerr)
		}
		return &rssDoc{}, fmt.Errorf("%s does not read, moved to %s; starting with no items seen: %v", p, bad, err)
	}
	return &doc, nil
}

// saveRSSState writes doc as dir's rss.json.
func saveRSSState(dir string, doc *rssDoc) error {
	doc.Version, doc.Saved = RSSStateVersion, time.Now().UTC()
	data, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	return writeAtomic(dir, rssStateFile, append(data, '\n'))
}
