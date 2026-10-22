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
	keys := make([]string, 0, len(urls))
	for _, u := range urls {
		k, err := urlKey(u)
		if err != nil {
			return 0, err
		}
		keys = append(keys, k)
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
		if all {
			n += s.clear()
			continue
		}
		purged := map[string]bool{}
		for _, tag := range tags {
			for _, k := range s.tags.keys(tag) {
				if !purged[k] && s.remove(k) {
					purged[k] = true
				}
			}
		}
		for _, k := range keys {
			if !purged[k] && s.remove(k) {
				purged[k] = true
			}
		}
		n += len(purged)
	}
	return n, nil
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
