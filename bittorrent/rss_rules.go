package bittorrent

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/lesismal/fit/config"
)

// The RSS rules (I208), compiled when the config is applied: an item is
// picked by the first rule, in the config's order, whose feeds, patterns
// and size bounds it meets; a rule with episodes leaves out an episode of a
// show it added already.

// rssRule is a rule compiled.
type rssRule struct {
	name     string
	feeds    map[string]bool // nil: every feed
	include  []*regexp.Regexp
	exclude  []*regexp.Regexp
	category string
	paused   bool
	minSize  int64
	maxSize  int64
	episodes bool
	perPoll  int
	// on is the config's enabled.
	on bool
}

// compileRules compiles the config's rules, which its check has passed.
func compileRules(rules []config.BitTorrentRSSRule) ([]*rssRule, error) {
	out := make([]*rssRule, 0, len(rules))
	for i := range rules {
		c := &rules[i]
		r := &rssRule{name: c.Name, category: c.Category, paused: c.Paused, minSize: c.MinSize, maxSize: c.MaxSize,
			episodes: c.Episodes, perPoll: c.PerPoll(), on: c.On()}
		if len(c.Feeds) > 0 {
			r.feeds = make(map[string]bool, len(c.Feeds))
			for _, f := range c.Feeds {
				r.feeds[f] = true
			}
		}
		for _, l := range []struct {
			src []string
			dst *[]*regexp.Regexp
		}{{c.Patterns, &r.include}, {c.Exclude, &r.exclude}} {
			for _, p := range l.src {
				re, err := config.TitlePattern(p)
				if err != nil {
					return nil, fmt.Errorf("rss rule %s: %q: %w", c.Name, p, err)
				}
				*l.dst = append(*l.dst, re)
			}
		}
		out = append(out, r)
	}
	return out, nil
}

// matches reports whether the rule picks it, an item of feed.
func (r *rssRule) matches(feed string, it *feedItem) bool {
	if r.feeds != nil && !r.feeds[feed] {
		return false
	}
	if len(r.include) > 0 {
		ok := false
		for _, re := range r.include {
			if re.MatchString(it.Title) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	for _, re := range r.exclude {
		if re.MatchString(it.Title) {
			return false
		}
	}
	if it.Size > 0 && (r.minSize > 0 && it.Size < r.minSize || r.maxSize > 0 && it.Size > r.maxSize) {
		return false
	}
	return true
}

var (
	// S01E02, S01E02E03, S01E02-E03, S01E02-03, S01.E02.
	episodeSE = regexp.MustCompile(`(?i)\bS(\d{1,4})[ ._-]?E(\d{1,4})(?:(?:-|[ ._]?E)E?(\d{1,4}))?`)
	// 1x02.
	episodeX = regexp.MustCompile(`(?i)\b(\d{1,2})x(\d{2,3})\b`)
	// 2026-11-09, 2026.11.09, 2026 11 09.
	episodeDate = regexp.MustCompile(`\b((?:19|20)\d{2})[-._ /](0[1-9]|1[0-2])[-._ /](0[1-9]|[12]\d|3[01])\b`)
	// A release group's tags before the title: [Group], (Group), 【Group】.
	leadingTags = regexp.MustCompile(`^(?:\s*(?:\[[^\]]*\]|\([^)]*\)|【[^】]*】))+`)
)

// episodeKey returns the show and episode a title names, as one key:
// "<show>|s01e02", "<show>|s01e02-e03" or "<show>|2026-11-09", the show
// being the title before the episode, in lower case, its punctuation made
// spaces and a release group's leading tags left out.
func episodeKey(title string) (string, bool) {
	var ep string
	start := -1
	if m := episodeSE.FindStringSubmatchIndex(title); m != nil {
		s, _ := strconv.Atoi(title[m[2]:m[3]])
		e, _ := strconv.Atoi(title[m[4]:m[5]])
		ep = fmt.Sprintf("s%02de%02d", s, e)
		if m[6] >= 0 {
			if e2, _ := strconv.Atoi(title[m[6]:m[7]]); e2 > e && e2-e <= 50 {
				// A range of episodes, not a resolution after a dash.
				ep += fmt.Sprintf("-e%02d", e2)
			}
		}
		start = m[0]
	} else if m := episodeX.FindStringSubmatchIndex(title); m != nil {
		s, _ := strconv.Atoi(title[m[2]:m[3]])
		e, _ := strconv.Atoi(title[m[4]:m[5]])
		ep, start = fmt.Sprintf("s%02de%02d", s, e), m[0]
	} else if m := episodeDate.FindStringSubmatchIndex(title); m != nil {
		ep, start = title[m[2]:m[3]]+"-"+title[m[4]:m[5]]+"-"+title[m[6]:m[7]], m[0]
	}
	if start < 0 {
		return "", false
	}
	return showName(title[:start]) + "|" + ep, true
}

// showName normalizes the part of a title before its episode.
func showName(s string) string {
	s = leadingTags.ReplaceAllString(s, "")
	var b strings.Builder
	space := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			if space && b.Len() > 0 {
				b.WriteByte(' ')
			}
			b.WriteRune(r)
			space = false
		} else {
			space = true
		}
	}
	return b.String()
}
