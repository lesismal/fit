package cache

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
)

// ErrNoZone is what Purge returns for a zone no cache uses.
var ErrNoZone = errors.New("no cache uses the zone")

// Purge removes what the zone named zone keeps, or every zone when zone is
// "": the responses to urls, absolute URLs as requests name them, every
// variant of each; the responses tagged with any of tags, by their
// Cache-Tag or Surrogate-Key fields; or, with all, everything. It removes
// them from memory and from disk, where a write still queued is dropped
// too, and returns how many keys it removed.
func Purge(zone string, urls, tags []string, all bool) (int, error) {
	return PurgeBy(zone, Selection{URLs: urls, Tags: tags, All: all})
}

// Selection is what PurgeBy removes: the responses to URLs, those whose
// URLs start with any of Prefixes, those tagged with any of Tags, or, with
// All, everything.
type Selection struct {
	URLs     []string
	Prefixes []string
	Tags     []string
	All      bool
}

// PurgeBy is Purge, by sel, which may also name prefixes: absolute http or
// https URLs whose host is compared as a request's (in lower case) and
// whose path and query start the keys removed, "https://example.com/img/"
// removing every response to a URL under /img/ on that host. A prefix is
// found by going through the keys the zone keeps, in memory and on disk,
// each part of the memory and the disk's index under its lock for as long
// as reading its keys takes, as clearing them takes.
func PurgeBy(zone string, sel Selection) (int, error) {
	keys := make([]string, 0, len(sel.URLs))
	for _, u := range sel.URLs {
		k, err := urlKey(u)
		if err != nil {
			return 0, err
		}
		keys = append(keys, k)
	}
	prefixes := make([]string, 0, len(sel.Prefixes))
	for _, p := range sel.Prefixes {
		k, err := prefixKey(p)
		if err != nil {
			return 0, err
		}
		prefixes = append(prefixes, k)
	}
	zones.Lock()
	var stores []*store
	for name, s := range zones.m {
		if zone == "" || name == zone {
			stores = append(stores, s)
		}
	}
	zones.Unlock()
	if zone != "" && len(stores) == 0 {
		return 0, fmt.Errorf("%w %q", ErrNoZone, zone)
	}
	n := 0
	for _, s := range stores {
		if sel.All {
			n += s.clear()
			continue
		}
		purged := map[string]bool{}
		remove := func(k string) {
			if !purged[k] && s.remove(k) {
				purged[k] = true
			}
		}
		for _, tag := range sel.Tags {
			for _, k := range s.tags.keys(tag) {
				remove(k)
			}
		}
		for _, k := range keys {
			remove(k)
		}
		if len(prefixes) > 0 {
			for _, k := range s.keysWith(prefixes) {
				remove(k)
			}
		}
		n += len(purged)
	}
	return n, nil
}

// prefixKey returns the start of the keys of the responses to requests for
// URLs that start with u: its scheme, its host in lower case, and its path
// and query as written, "/" when it has no path.
func prefixKey(u string) (string, error) {
	p, err := url.Parse(u)
	if err != nil || p.Host == "" || p.Scheme != "http" && p.Scheme != "https" || p.User != nil || p.Fragment != "" {
		return "", fmt.Errorf("%q is not the start of an absolute http or https URL", u)
	}
	return p.Scheme + "://" + strings.ToLower(p.Host) + p.RequestURI(), nil
}

// keysWith returns the keys the store keeps, in memory or on disk, that
// start with any of prefixes.
func (s *store) keysWith(prefixes []string) []string {
	match := func(k string) bool {
		for _, p := range prefixes {
			if strings.HasPrefix(k, p) {
				return true
			}
		}
		return false
	}
	var out []string
	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.Lock()
		for k := range sh.items {
			if match(k) {
				out = append(out, k)
			}
		}
		sh.mu.Unlock()
	}
	if d := s.disk.Load(); d != nil {
		d.mu.Lock()
		for k, it := range d.items {
			if len(it.variants) > 0 && match(k) {
				out = append(out, k)
			}
		}
		d.mu.Unlock()
	}
	return out
}

// urlKey returns the key of the response to a request for u.
func urlKey(u string) (string, error) {
	p, err := url.Parse(u)
	if err != nil || p.Host == "" || p.Scheme != "http" && p.Scheme != "https" {
		return "", fmt.Errorf("%q is not an absolute http or https URL", u)
	}
	return p.Scheme + "://" + strings.ToLower(p.Host) + p.RequestURI(), nil
}

// ZoneStats is what a zone keeps.
type ZoneStats struct {
	Zone        string `json:"zone"`
	Keys        int    `json:"keys"`
	Bytes       int64  `json:"bytes"`
	DiskEntries int    `json:"disk_entries,omitempty"`
	DiskBytes   int64  `json:"disk_bytes,omitempty"`
	DiskPath    string `json:"disk_path,omitempty"`
}

// Zones returns what each zone in use keeps, by name.
func Zones() []ZoneStats {
	zones.Lock()
	stores := make([]*store, 0, len(zones.m))
	for _, s := range zones.m {
		stores = append(stores, s)
	}
	zones.Unlock()
	stats := make([]ZoneStats, 0, len(stores))
	for _, s := range stores {
		z := ZoneStats{Zone: s.name}
		z.Keys, z.Bytes = s.stats()
		if d := s.disk.Load(); d != nil {
			z.DiskEntries, z.DiskBytes = d.stats()
			z.DiskPath = d.dir
		}
		stats = append(stats, z)
	}
	slices.SortFunc(stats, func(a, b ZoneStats) int { return strings.Compare(a.Zone, b.Zone) })
	return stats
}
