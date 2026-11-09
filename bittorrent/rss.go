package bittorrent

import (
	"context"
	"errors"
	"fmt"
	stdhttp "net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"time"

	fibhttp "github.com/lesismal/fib/http"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/observe"
)

// RSS auto-download (I208): the feeds of bittorrent.client's rss, polled on
// their intervals by a goroutine of the role's own, at most MaxRSSPolls at
// once, each poll on a goroutine of its own: the feed downloaded with fib's
// asynchronous HTTP client (whose callback only hands the answer over),
// conditionally with the ETag and Last-Modified it answered last, at most
// max_feed_bytes, within timeout, following at most 5 redirects; read
// (rss_parse.go); its new items given to the rules (rss_rules.go); the
// torrents of those picked added by Add (a magnet link) or addURL (a
// .torrent file's URL, by I198's rules). Every item is remembered as seen,
// in data_dir/rss.json (rss_state.go), but those a rule's max_per_poll left
// for the next poll and those whose add may pass later (the client down,
// max_torrents, too many URL downloads at once): their feed's ETag and
// Last-Modified are not kept then, so that the next poll reads them again.
// Nothing here runs on a fib worker. Locks: r.mu before rss.mu, never the
// other way; rss.mu is never held while adding or downloading.

const (
	// MaxRSSPolls bounds the feeds polled at once.
	MaxRSSPolls = 4
	// rssMaxAttempts is how many polls try an item whose .torrent file
	// could not be downloaded before it is given up.
	rssMaxAttempts = 3
	// maxRSSItemsPage bounds a page of RSSItems.
	maxRSSItemsPage = 1000
)

// The outcomes of an item.
const (
	// RSSAdded: a rule picked it and its torrent was added.
	RSSAdded = "added"
	// RSSExists: a rule picked it and its torrent is held already.
	RSSExists = "exists"
	// RSSDuplicate: a rule picked it, an episode the rule added already.
	RSSDuplicate = "duplicate"
	// RSSUnmatched: no rule picked it.
	RSSUnmatched = "unmatched"
	// RSSNoSource: a rule picked it, but it has no magnet link or URL.
	RSSNoSource = "no_source"
	// RSSFailed: its torrent could not be added (Error says why).
	RSSFailed = "failed"
)

// The results of a poll.
const (
	RSSResultOK          = "ok"
	RSSResultNotModified = "not_modified"
	RSSResultError       = "error"
)

var (
	// ErrRSSNoFeed is a feed the config does not have.
	ErrRSSNoFeed = errors.New("bittorrent: no such feed")
	// ErrRSSNoRule is a rule the config does not have.
	ErrRSSNoRule = errors.New("bittorrent: no such rule")
	// ErrRSSBusy is a poll of a feed being polled.
	ErrRSSBusy = errors.New("bittorrent: the feed is being polled")
)

// RSSItem is an item of a feed as the role remembers it.
type RSSItem struct {
	// Feed is its feed's name (in the API's answers).
	Feed  string    `json:"feed,omitempty"`
	Key   string    `json:"key"`
	Title string    `json:"title"`
	Seen  time.Time `json:"seen"`
	// Outcome is RSSAdded, RSSExists, RSSDuplicate, RSSUnmatched,
	// RSSNoSource or RSSFailed.
	Outcome string `json:"outcome"`
	// Rule is the rule that picked it, Torrent the torrent's ID.
	Rule    string `json:"rule,omitempty"`
	Torrent string `json:"torrent,omitempty"`
	Size    int64  `json:"size,omitempty"`
	// Source is how it was added: "magnet" or "url".
	Source string `json:"source,omitempty"`
	Error  string `json:"error,omitempty"`
	// Attempts counts the polls that tried to add it; one failed with
	// fewer than 3 is tried again.
	Attempts int `json:"attempts,omitempty"`
}

// RSSFeedStatus is a feed as the API reports it.
type RSSFeedStatus struct {
	Name string `json:"name"`
	// URL is the feed's URL without its user and query, where a passkey
	// may be.
	URL      string        `json:"url"`
	Title    string        `json:"title,omitempty"`
	Enabled  bool          `json:"enabled"`
	Toggled  bool          `json:"toggled,omitempty"`
	Interval time.Duration `json:"interval"`
	Polling  bool          `json:"polling,omitempty"`
	LastPoll time.Time     `json:"last_poll,omitzero"`
	NextPoll time.Time     `json:"next_poll,omitzero"`
	// Last is the last poll's result, since fit started, or its result
	// kept from before.
	Last       *RSSPollResult `json:"last,omitempty"`
	LastResult string         `json:"last_result,omitempty"`
	LastError  string         `json:"last_error,omitempty"`
	// Seen counts the items remembered; Polls, Errors, Added and Failed
	// count since fit started.
	Seen   int    `json:"seen"`
	Polls  uint64 `json:"polls"`
	Errors uint64 `json:"errors"`
	Added  uint64 `json:"added"`
	Failed uint64 `json:"failed"`
}

// RSSRuleStatus is a rule as the API reports it.
type RSSRuleStatus struct {
	Name     string   `json:"name"`
	Enabled  bool     `json:"enabled"`
	Toggled  bool     `json:"toggled,omitempty"`
	Feeds    []string `json:"feeds,omitempty"`
	Category string   `json:"category,omitempty"`
	Episodes bool     `json:"episodes,omitempty"`
	// EpisodesSeen counts the episodes remembered; Added the torrents it
	// added, kept across restarts.
	EpisodesSeen int       `json:"episodes_seen"`
	Added        int64     `json:"added"`
	LastAdded    time.Time `json:"last_added,omitzero"`
}

// RSSStatus is the RSS auto-download as the API reports it.
type RSSStatus struct {
	Configured bool            `json:"configured"`
	Feeds      []RSSFeedStatus `json:"feeds"`
	Rules      []RSSRuleStatus `json:"rules"`
	// Error is the last error saving rss.json, if any.
	Error string `json:"error,omitempty"`
}

// RSSPollResult is what a poll of a feed did.
type RSSPollResult struct {
	Feed   string    `json:"feed"`
	Time   time.Time `json:"time"`
	Result string    `json:"result"`
	// Status is the HTTP status answered last, 0 when none was.
	Status int    `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
	// Items counts the items read; New those not seen before; Added the
	// torrents added; Failed the items that failed; Deferred those left
	// for the next poll, by max_per_poll or an add that may pass later.
	Items     int  `json:"items"`
	New       int  `json:"new"`
	Added     int  `json:"added"`
	Failed    int  `json:"failed"`
	Deferred  int  `json:"deferred"`
	Truncated bool `json:"truncated,omitempty"`
}

// rssFeed is a feed's state.
type rssFeed struct {
	doc     *rssFeedDoc
	index   map[string]int // by key, into doc.Items
	polling bool
	last    *RSSPollResult
	// The counters since fit started.
	polls, errors, added, failed uint64
	notModified                  uint64
}

// rssRuleSt is a rule's state.
type rssRuleSt struct {
	doc *rssRuleDoc
	eps map[string]bool
}

// rssRunner is the role's RSS auto-download.
type rssRunner struct {
	r *Role

	// saveMu orders the saves.
	saveMu sync.Mutex

	mu      sync.Mutex
	cfg     *config.BitTorrentRSS // nil: none
	rules   []*rssRule
	dir     string
	loaded  bool
	feeds   map[string]*rssFeed
	ruleSts map[string]*rssRuleSt
	dirty   bool
	saveErr string
	httpc   *fibhttp.Client
	httpKey string
	ctx     context.Context
	cancel  context.CancelCauseFunc
	stopped bool
	loop    chan struct{} // closed when the loop ends
	polls   sync.WaitGroup
	sem     chan struct{}
}

func newRSS(r *Role) *rssRunner {
	ctx, cancel := context.WithCancelCause(context.Background())
	return &rssRunner{r: r, feeds: map[string]*rssFeed{}, ruleSts: map[string]*rssRuleSt{}, ctx: ctx, cancel: cancel,
		sem: make(chan struct{}, MaxRSSPolls)}
}

// apply takes cfg's rss, its state in dataDir. r.mu is held.
func (s *rssRunner) apply(cfg *config.BitTorrentClient, dataDir string) error {
	rules, err := compileRules(nil)
	if cfg.RSS != nil {
		if rules, err = compileRules(cfg.RSS.Rules); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return nil
	}
	if !s.loaded || filepath.Clean(dataDir) != filepath.Clean(s.dir) {
		doc, err := loadRSSState(dataDir)
		if err != nil && s.r.log != nil {
			s.r.log.Errorf("rss: %v", err)
		}
		s.dir, s.loaded = dataDir, true
		s.feeds, s.ruleSts = map[string]*rssFeed{}, map[string]*rssRuleSt{}
		for name, f := range doc.Feeds {
			if f != nil {
				s.feeds[name] = newFeedState(f)
			}
		}
		for name, rd := range doc.Rules {
			if rd != nil {
				s.ruleSts[name] = newRuleState(rd)
			}
		}
	}
	s.cfg, s.rules = cfg.RSS, rules
	// The state of the feeds and rules the config has, and none of those
	// it no longer has.
	keepF, keepR := map[string]bool{}, map[string]bool{}
	if s.cfg != nil {
		for i := range s.cfg.Feeds {
			name := s.cfg.Feeds[i].Name
			keepF[name] = true
			f := s.feeds[name]
			if f == nil {
				s.feeds[name] = newFeedState(&rssFeedDoc{})
			} else if s.trimLocked(f) {
				s.dirty = true
			}
		}
		for _, rl := range rules {
			keepR[rl.name] = true
			if s.ruleSts[rl.name] == nil {
				s.ruleSts[rl.name] = newRuleState(&rssRuleDoc{})
			}
		}
	}
	for name := range s.feeds {
		if !keepF[name] {
			delete(s.feeds, name)
			s.dirty = true
		}
	}
	for name := range s.ruleSts {
		if !keepR[name] {
			delete(s.ruleSts, name)
			s.dirty = true
		}
	}
	if s.httpc != nil && (s.cfg == nil || s.httpKeyOf() != s.httpKey) {
		s.httpc.Close()
		s.httpc = nil
	}
	if s.loop == nil && s.cfg != nil && len(s.cfg.Feeds) > 0 {
		s.loop = make(chan struct{})
		go s.run(s.loop)
	}
	return nil
}

func newFeedState(doc *rssFeedDoc) *rssFeed {
	f := &rssFeed{doc: doc}
	f.reindex()
	return f
}

func (f *rssFeed) reindex() {
	f.index = make(map[string]int, len(f.doc.Items))
	for i, it := range f.doc.Items {
		f.index[it.Key] = i
	}
}

func newRuleState(doc *rssRuleDoc) *rssRuleSt {
	if len(doc.Episodes) > MaxRSSEpisodes {
		doc.Episodes = doc.Episodes[len(doc.Episodes)-MaxRSSEpisodes:]
	}
	st := &rssRuleSt{doc: doc, eps: make(map[string]bool, len(doc.Episodes))}
	for _, e := range doc.Episodes {
		st.eps[e] = true
	}
	return st
}

// trimLocked forgets the oldest items of f past history_size, and reports
// whether it did. s.mu is held.
func (s *rssRunner) trimLocked(f *rssFeed) bool {
	n := s.cfg.History()
	if len(f.doc.Items) <= n {
		return false
	}
	f.doc.Items = slices.Clone(f.doc.Items[len(f.doc.Items)-n:])
	f.reindex()
	return true
}

// httpKeyOf is what the feeds' HTTP client is made by. s.mu is held.
func (s *rssRunner) httpKeyOf() string {
	return fmt.Sprint(s.cfg.FetchTimeout(), "|", s.cfg.FeedBytes())
}

// clientLocked returns the feeds' HTTP client, made on first use. s.mu is
// held.
func (s *rssRunner) clientLocked() *fibhttp.Client {
	if s.httpc == nil {
		cc := fibhttp.DefaultClientConfig()
		cc.Timeout = s.cfg.FetchTimeout()
		cc.DialTimeout = min(cc.Timeout, 10*time.Second)
		cc.MaxResponseBodyBytes = s.cfg.FeedBytes()
		cc.MaxResponseHeaderBytes = 64 << 10
		cc.MaxConnsPerHost, cc.MaxPendingPerHost, cc.MaxIdleConnsPerHost = 2, MaxRSSPolls, 1
		cc.IdleConnTimeout = 30 * time.Second
		s.httpc, s.httpKey = fibhttp.NewClient(s.r.key, cc), s.httpKeyOf()
	}
	return s.httpc
}

// stop ends the loop and the polls, and saves the state. r.mu is not held.
func (s *rssRunner) stop() {
	s.mu.Lock()
	s.stopped = true
	loop := s.loop
	s.mu.Unlock()
	s.cancel(errors.New("the role stopped"))
	if loop != nil {
		<-loop
	}
	s.polls.Wait()
	s.mu.Lock()
	if s.httpc != nil {
		s.httpc.Close()
		s.httpc = nil
	}
	s.mu.Unlock()
	s.save()
}

// run starts the polls of the feeds due, every tick, until the role stops.
func (s *rssRunner) run(done chan struct{}) {
	defer close(done)
	t := time.NewTicker(s.r.tick)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
		}
		s.startDue()
	}
}

// startDue starts a poll of each enabled feed due, while fewer than
// MaxRSSPolls run.
func (s *rssRunner) startDue() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg == nil || s.stopped {
		return
	}
	now := s.r.now()
	for i := range s.cfg.Feeds {
		fc := &s.cfg.Feeds[i]
		f := s.feeds[fc.Name]
		if f == nil || f.polling || !s.feedOnLocked(fc, f) {
			continue
		}
		if !f.doc.LastPoll.IsZero() && now.Before(f.doc.LastPoll.Add(fc.Every())) {
			continue
		}
		select {
		case s.sem <- struct{}{}:
		default:
			return // the next tick
		}
		f.polling = true
		s.polls.Add(1)
		name := fc.Name
		go func() {
			defer s.polls.Done()
			defer func() { <-s.sem }()
			s.poll(s.ctx, name)
		}()
	}
}

// feedOnLocked reports whether feed fc is polled: the API's toggle, else
// the config's enabled. s.mu is held.
func (s *rssRunner) feedOnLocked(fc *config.BitTorrentRSSFeed, f *rssFeed) bool {
	if f.doc.Enabled != nil {
		return *f.doc.Enabled
	}
	return fc.On()
}

// ruleOnLocked reports whether rule rl is used. s.mu is held.
func (s *rssRunner) ruleOnLocked(rl *rssRule) bool {
	if st := s.ruleSts[rl.name]; st != nil && st.doc.Enabled != nil {
		return *st.doc.Enabled
	}
	return rl.on
}

// rssPlan is an item a poll adds.
type rssPlan struct {
	item feedItem
	rule *rssRule
	ep   string
}

// poll polls the feed named name once, which the caller marked polling,
// and returns what it did.
func (s *rssRunner) poll(ctx context.Context, name string) RSSPollResult {
	res := RSSPollResult{Feed: name, Time: s.r.now().UTC()}
	s.mu.Lock()
	fc, f := s.feedConfigLocked(name)
	if fc == nil || f == nil || s.stopped {
		if f != nil {
			f.polling = false
		}
		s.mu.Unlock()
		res.Result, res.Error = RSSResultError, ErrRSSNoFeed.Error()
		return res
	}
	c := s.clientLocked()
	timeout := s.cfg.FetchTimeout()
	etag, lastMod := f.doc.ETag, f.doc.LastModified
	maxItems := fc.Items()
	feedURL := fc.URL
	hdr := stdhttp.Header{"Accept": {"application/rss+xml, application/atom+xml, application/xml;q=0.9, text/xml;q=0.9, */*;q=0.5"}}
	for k, v := range fc.Headers {
		hdr.Set(k, v)
	}
	for k, env := range fc.HeadersEnv {
		if v, ok := os.LookupEnv(env); ok {
			hdr.Set(k, v)
		}
	}
	s.mu.Unlock()

	a, err := s.fetchFeed(ctx, c, feedURL, timeout, hdr, etag, lastMod)
	res.Status = a.status
	var parsed *parsedFeed
	if err == nil && a.status == stdhttp.StatusOK {
		parsed, err = parseFeed(a.body, maxItems)
	}
	if err != nil {
		res.Result, res.Error = RSSResultError, err.Error()
		s.finish(name, &res, nil, "", "")
		return res
	}
	if a.status == stdhttp.StatusNotModified {
		res.Result = RSSResultNotModified
		s.finish(name, &res, nil, etag, lastMod)
		return res
	}
	res.Result, res.Items, res.Truncated = RSSResultOK, len(parsed.Items), parsed.Truncated

	// Which items are new, and which rule picks each.
	now := s.r.now().UTC()
	var plans []rssPlan
	var seen []RSSItem
	s.mu.Lock()
	f = s.feeds[name]
	if f == nil {
		s.mu.Unlock()
		return res
	}
	f.doc.Title = parsed.Title
	perRule := map[string]int{}
	pendingEps := map[string]bool{}
	inPoll := make(map[string]bool, len(parsed.Items))
	for _, it := range parsed.Items {
		if inPoll[it.Key] {
			continue // the same item twice in the document
		}
		inPoll[it.Key] = true
		if i, ok := f.index[it.Key]; ok {
			old := f.doc.Items[i]
			if old.Outcome != RSSFailed || old.Attempts >= rssMaxAttempts {
				continue
			}
		}
		res.New++
		rec := RSSItem{Key: it.Key, Title: it.Title, Seen: now, Size: it.Size}
		var rule *rssRule
		for _, rl := range s.rules {
			if s.ruleOnLocked(rl) && rl.matches(name, &it) {
				rule = rl
				break
			}
		}
		if rule == nil {
			rec.Outcome = RSSUnmatched
			seen = append(seen, rec)
			continue
		}
		rec.Rule = rule.name
		if _, _, ok := it.source(); !ok {
			rec.Outcome = RSSNoSource
			seen = append(seen, rec)
			continue
		}
		var ep string
		if rule.episodes {
			if k, ok := episodeKey(it.Title); ok {
				ep = k
				if s.ruleSts[rule.name].eps[k] || pendingEps[rule.name+"\x00"+k] {
					rec.Outcome = RSSDuplicate
					seen = append(seen, rec)
					continue
				}
			}
		}
		if perRule[rule.name] >= rule.perPoll {
			res.Deferred++
			continue
		}
		perRule[rule.name]++
		if ep != "" {
			pendingEps[rule.name+"\x00"+ep] = true
		}
		plans = append(plans, rssPlan{item: it, rule: rule, ep: ep})
	}
	s.mu.Unlock()

	// The adds, without s.mu.
	type done struct {
		rssPlan
		rec RSSItem
	}
	var dones []done
	for _, p := range plans {
		if ctx.Err() != nil {
			res.Deferred++
			continue
		}
		rec := RSSItem{Key: p.item.Key, Title: p.item.Title, Seen: now, Size: p.item.Size, Rule: p.rule.name}
		magnet, link, _ := p.item.source()
		req := AddRequest{Category: p.rule.category, Paused: p.rule.paused}
		var st Status
		var err error
		if magnet != "" {
			rec.Source, req.Magnet = "magnet", magnet
			st, err = s.r.Add(req)
		} else {
			rec.Source = "url"
			st, err = s.r.addURL(ctx, link, req)
		}
		switch {
		case err == nil:
			rec.Outcome, rec.Torrent = RSSAdded, st.ID
			res.Added++
			e := Event{Type: EventRSSAdded, Torrent: st.ID, Name: st.Name, Category: st.Category, State: st.State,
				Feed: name, Rule: p.rule.name, Title: p.item.Title}
			s.r.event(e)
		case errors.Is(err, ErrExists):
			rec.Outcome = RSSExists
		case errors.Is(err, ErrBadRequest):
			rec.Outcome, rec.Error, rec.Attempts = RSSFailed, err.Error(), rssMaxAttempts
			res.Failed++
		case errors.Is(err, ErrFetch) && ctx.Err() == nil:
			rec.Outcome, rec.Error = RSSFailed, err.Error()
			res.Failed++
		default:
			// It may pass later: max_torrents, too many URL downloads,
			// the client down, the role stopping.
			res.Deferred++
			if res.Error == "" {
				res.Error = err.Error()
			}
			continue
		}
		dones = append(dones, done{p, rec})
	}

	// Remember what was seen and added; the ETag and Last-Modified only
	// when nothing was left for later.
	s.mu.Lock()
	if f = s.feeds[name]; f != nil {
		for _, d := range dones {
			if d.rec.Outcome == RSSFailed {
				if i, ok := f.index[d.rec.Key]; ok {
					d.rec.Attempts = max(d.rec.Attempts, f.doc.Items[i].Attempts+1)
				} else {
					d.rec.Attempts = max(d.rec.Attempts, 1)
				}
			}
			seen = append(seen, d.rec)
			if d.rec.Outcome == RSSAdded || d.rec.Outcome == RSSExists {
				st := s.ruleSts[d.rule.name]
				if st == nil {
					continue
				}
				if d.ep != "" && !st.eps[d.ep] {
					st.eps[d.ep] = true
					st.doc.Episodes = append(st.doc.Episodes, d.ep)
					if len(st.doc.Episodes) > MaxRSSEpisodes {
						delete(st.eps, st.doc.Episodes[0])
						st.doc.Episodes = slices.Clone(st.doc.Episodes[1:])
					}
				}
				if d.rec.Outcome == RSSAdded {
					st.doc.Added++
					st.doc.LastAdded = now
				}
			}
		}
	}
	s.mu.Unlock()
	newETag, newLM := a.etag, a.lastModified
	if res.Deferred > 0 {
		newETag, newLM = etag, lastMod
	}
	s.finish(name, &res, seen, newETag, newLM)
	return res
}

// finish records a poll's result and the items it saw, and saves the
// state.
func (s *rssRunner) finish(name string, res *RSSPollResult, seen []RSSItem, etag, lastMod string) {
	s.mu.Lock()
	f := s.feeds[name]
	if f == nil {
		s.mu.Unlock()
		return
	}
	f.polling = false
	f.polls++
	switch res.Result {
	case RSSResultError:
		f.errors++
	case RSSResultNotModified:
		f.notModified++
	}
	f.added += uint64(res.Added)
	f.failed += uint64(res.Failed)
	f.last = res
	f.doc.LastPoll, f.doc.LastResult, f.doc.LastError = res.Time, res.Result, res.Error
	if res.Result != RSSResultError {
		f.doc.ETag, f.doc.LastModified = clip(etag, 1024), clip(lastMod, 256)
	}
	for _, it := range seen {
		if i, ok := f.index[it.Key]; ok {
			// Tried again: it moves to the end, as the newest.
			f.doc.Items = slices.Delete(f.doc.Items, i, i+1)
			f.reindex()
		}
		f.index[it.Key] = len(f.doc.Items)
		f.doc.Items = append(f.doc.Items, it)
	}
	if s.cfg != nil {
		s.trimLocked(f)
	}
	s.dirty = true
	log := s.r.log
	s.mu.Unlock()
	if log != nil {
		switch res.Result {
		case RSSResultError:
			log.Warnf("rss feed %s: %s", name, res.Error)
		case RSSResultOK:
			if res.Added > 0 || res.Failed > 0 {
				log.Infof("rss feed %s: %d items, %d new, %d added, %d failed, %d left for later", name, res.Items, res.New, res.Added, res.Failed, res.Deferred)
			} else {
				log.Debugf("rss feed %s: %d items, %d new", name, res.Items, res.New)
			}
		}
	}
	s.save()
}

// fetchFeed downloads the feed at rawURL, following at most 5 redirects,
// http or https only: its answer, 200 with a body or 304.
func (s *rssRunner) fetchFeed(ctx context.Context, c *fibhttp.Client, rawURL string, timeout time.Duration, hdr stdhttp.Header, etag, lastMod string) (getAnswer, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return getAnswer{}, fmt.Errorf("%w: %v", ErrFetch, err)
	}
	if etag != "" {
		hdr.Set("If-None-Match", etag)
	}
	if lastMod != "" {
		hdr.Set("If-Modified-Since", lastMod)
	}
	deadline := time.Now().Add(timeout)
	for range maxRedirects + 1 {
		a, err := httpGet(ctx, c, u, time.Until(deadline), hdr)
		if err != nil {
			return a, err
		}
		if a.status == stdhttp.StatusOK || a.status == stdhttp.StatusNotModified {
			return a, nil
		}
		loc, err := redirectOf(u, a)
		if err != nil {
			return a, err
		}
		if loc.Scheme != "http" && loc.Scheme != "https" {
			return a, fmt.Errorf("%w: %s redirects to %q", ErrFetch, u.Redacted(), loc.Scheme+":")
		}
		u = loc
	}
	return getAnswer{}, fmt.Errorf("%w: more than %d redirects", ErrFetch, maxRedirects)
}

// feedConfigLocked returns the config and the state of feed name. s.mu is
// held.
func (s *rssRunner) feedConfigLocked(name string) (*config.BitTorrentRSSFeed, *rssFeed) {
	if s.cfg == nil {
		return nil, nil
	}
	fc := s.cfg.Feed(name)
	if fc == nil {
		return nil, nil
	}
	return fc, s.feeds[name]
}

// save writes rss.json when something changed.
func (s *rssRunner) save() {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()
	s.mu.Lock()
	if !s.dirty || !s.loaded {
		s.mu.Unlock()
		return
	}
	doc := &rssDoc{Feeds: make(map[string]*rssFeedDoc, len(s.feeds)), Rules: make(map[string]*rssRuleDoc, len(s.ruleSts))}
	for name, f := range s.feeds {
		d := *f.doc
		d.Items = slices.Clone(d.Items)
		doc.Feeds[name] = &d
	}
	for name, rs := range s.ruleSts {
		d := *rs.doc
		d.Episodes = slices.Clone(d.Episodes)
		doc.Rules[name] = &d
	}
	dir := s.dir
	s.dirty = false
	s.mu.Unlock()
	err := saveRSSState(dir, doc)
	s.mu.Lock()
	defer s.mu.Unlock()
	msg := ""
	if err != nil {
		msg = err.Error()
		s.dirty = true // the next save tries again
	}
	if msg != s.saveErr && s.r.log != nil {
		if msg != "" {
			s.r.log.Errorf("rss: saving %s: %s", filepath.Join(dir, rssStateFile), msg)
		} else {
			s.r.log.Infof("rss: %s is saved again", filepath.Join(dir, rssStateFile))
		}
	}
	s.saveErr = msg
}

// The role's RSS API, for the Admin API, the CLI, the pages and the
// assistant. PollFeed waits for the poll: none of it may be called on a fib
// worker.

// RSS reports the feeds and the rules.
func (r *Role) RSS() RSSStatus {
	s := r.rss
	s.mu.Lock()
	defer s.mu.Unlock()
	out := RSSStatus{Configured: s.cfg != nil, Feeds: []RSSFeedStatus{}, Rules: []RSSRuleStatus{}, Error: s.saveErr}
	if s.cfg == nil {
		return out
	}
	for i := range s.cfg.Feeds {
		fc := &s.cfg.Feeds[i]
		f := s.feeds[fc.Name]
		if f == nil {
			continue
		}
		st := RSSFeedStatus{Name: fc.Name, URL: plainURL(fc.URL), Title: f.doc.Title, Enabled: s.feedOnLocked(fc, f),
			Toggled: f.doc.Enabled != nil, Interval: fc.Every(), Polling: f.polling, LastPoll: f.doc.LastPoll,
			LastResult: f.doc.LastResult, LastError: f.doc.LastError, Seen: len(f.doc.Items), Polls: f.polls,
			Errors: f.errors, Added: f.added, Failed: f.failed}
		if f.last != nil {
			l := *f.last
			st.Last = &l
		}
		if st.Enabled {
			if f.doc.LastPoll.IsZero() {
				st.NextPoll = r.now().UTC()
			} else {
				st.NextPoll = f.doc.LastPoll.Add(fc.Every())
			}
		}
		out.Feeds = append(out.Feeds, st)
	}
	for i, rl := range s.rules {
		c := &s.cfg.Rules[i]
		st := RSSRuleStatus{Name: rl.name, Enabled: s.ruleOnLocked(rl), Feeds: slices.Clone(c.Feeds), Category: rl.category,
			Episodes: rl.episodes}
		if rs := s.ruleSts[rl.name]; rs != nil {
			st.Toggled = rs.doc.Enabled != nil
			st.EpisodesSeen, st.Added, st.LastAdded = len(rs.doc.Episodes), rs.doc.Added, rs.doc.LastAdded
		}
		out.Rules = append(out.Rules, st)
	}
	return out
}

// RSSItems returns the items seen, the newest first, of feed (every feed
// when empty) and of outcome (every outcome when empty), at most limit
// (100 when 0, at most 1000), and how many match.
func (r *Role) RSSItems(feed, outcome string, limit int) ([]RSSItem, int, error) {
	if limit <= 0 {
		limit = 100
	}
	limit = min(limit, maxRSSItemsPage)
	s := r.rss
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cfg == nil {
		return []RSSItem{}, 0, nil
	}
	if feed != "" && s.cfg.Feed(feed) == nil {
		return nil, 0, ErrRSSNoFeed
	}
	var all []RSSItem
	for name, f := range s.feeds {
		if feed != "" && name != feed {
			continue
		}
		for _, it := range f.doc.Items {
			if outcome != "" && it.Outcome != outcome {
				continue
			}
			it.Feed = name
			all = append(all, it)
		}
	}
	sort.SliceStable(all, func(i, j int) bool {
		if !all[i].Seen.Equal(all[j].Seen) {
			return all[i].Seen.After(all[j].Seen)
		}
		if all[i].Feed != all[j].Feed {
			return all[i].Feed < all[j].Feed
		}
		return false
	})
	total := len(all)
	if len(all) > limit {
		all = all[:limit]
	}
	if all == nil {
		all = []RSSItem{}
	}
	return all, total, nil
}

// PollFeed polls the feed named name now, enabled or not, and returns what
// the poll did: ErrRSSNoFeed for a feed the config does not have,
// ErrRSSBusy while it is being polled. It waits for the poll.
func (r *Role) PollFeed(name string) (RSSPollResult, error) {
	s := r.rss
	s.mu.Lock()
	fc, f := s.feedConfigLocked(name)
	switch {
	case s.stopped:
		s.mu.Unlock()
		return RSSPollResult{}, ErrNotRunning
	case fc == nil || f == nil:
		s.mu.Unlock()
		return RSSPollResult{}, ErrRSSNoFeed
	case f.polling:
		s.mu.Unlock()
		return RSSPollResult{}, ErrRSSBusy
	}
	f.polling = true
	s.polls.Add(1)
	s.mu.Unlock()
	defer s.polls.Done()
	return s.poll(s.ctx, name), nil
}

// SetFeedEnabled turns feed name's polling on or off, whatever the config
// says, or back to the config with nil; data_dir keeps the choice.
func (r *Role) SetFeedEnabled(name string, on *bool) error {
	s := r.rss
	s.mu.Lock()
	fc, f := s.feedConfigLocked(name)
	if fc == nil || f == nil {
		s.mu.Unlock()
		return ErrRSSNoFeed
	}
	f.doc.Enabled = cloneBool(on)
	s.dirty = true
	s.mu.Unlock()
	s.save()
	return nil
}

// SetRuleEnabled turns rule name on or off, whatever the config says, or
// back to the config with nil; data_dir keeps the choice.
func (r *Role) SetRuleEnabled(name string, on *bool) error {
	s := r.rss
	s.mu.Lock()
	st := s.ruleSts[name]
	if s.cfg == nil || st == nil {
		s.mu.Unlock()
		return ErrRSSNoRule
	}
	st.doc.Enabled = cloneBool(on)
	s.dirty = true
	s.mu.Unlock()
	s.save()
	return nil
}

func cloneBool(b *bool) *bool {
	if b == nil {
		return nil
	}
	v := *b
	return &v
}

// plainURL returns raw without its user and query, where a passkey may be.
func plainURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	u.User, u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment = nil, "", false, "", ""
	return u.String()
}

// writeMetrics writes the feeds' counters.
func (s *rssRunner) writeMetrics(w observe.MetricWriter) {
	s.mu.Lock()
	type line struct {
		name                                   string
		ok, notModified, errors, added, failed uint64
	}
	var lines []line
	if s.cfg != nil {
		for i := range s.cfg.Feeds {
			if f := s.feeds[s.cfg.Feeds[i].Name]; f != nil {
				lines = append(lines, line{s.cfg.Feeds[i].Name, f.polls - f.errors - f.notModified, f.notModified, f.errors, f.added, f.failed})
			}
		}
	}
	s.mu.Unlock()
	if len(lines) == 0 {
		return
	}
	w.Family("fit_bittorrent_rss_polls_total", "counter", "Polls of an RSS feed since fit started, by result: ok, not_modified (304) or error.")
	for _, l := range lines {
		w.Sample("fit_bittorrent_rss_polls_total", float64(l.ok), "feed", l.name, "result", RSSResultOK)
		w.Sample("fit_bittorrent_rss_polls_total", float64(l.notModified), "feed", l.name, "result", RSSResultNotModified)
		w.Sample("fit_bittorrent_rss_polls_total", float64(l.errors), "feed", l.name, "result", RSSResultError)
	}
	w.Family("fit_bittorrent_rss_items_added_total", "counter", "Torrents the rules added from an RSS feed's items since fit started.")
	for _, l := range lines {
		w.Sample("fit_bittorrent_rss_items_added_total", float64(l.added), "feed", l.name)
	}
	w.Family("fit_bittorrent_rss_items_failed_total", "counter", "Items of an RSS feed a rule picked whose torrent could not be added, since fit started.")
	for _, l := range lines {
		w.Sample("fit_bittorrent_rss_items_failed_total", float64(l.failed), "feed", l.name)
	}
}
