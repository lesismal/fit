package bittorrent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Reading a feed (I208): RSS 2.0, RSS 1.0 (RDF) and Atom, with encoding/xml,
// from a body already bounded by max_feed_bytes. Each item gives an ID (its
// guid or id, else its link), a title, a size and a source: a magnet link,
// the URL of a .torrent file, or an info hash made into a magnet link. The
// items read are bounded by max_items, and every string by a bound of its
// own: a title is cut, a URL or magnet link past its bound is left out.

const (
	// maxRSSTitle bounds an item's title, cut past it, and maxRSSKey its
	// ID, hashed past it.
	maxRSSTitle = 1 << 10
	maxRSSKey   = 1 << 10
)

// ErrFeed is a feed that could not be read.
var ErrFeed = errors.New("bittorrent: the feed could not be read")

// feedItem is an item of a feed.
type feedItem struct {
	// Key tells the item from the others of its feed, across polls.
	Key   string
	Title string
	// Magnet is its magnet link, TorrentURL the URL of its .torrent file,
	// Link its page; the source is Magnet, else TorrentURL, else Link.
	Magnet     string
	TorrentURL string
	Link       string
	// Size is its content's size, 0 when the feed does not say.
	Size      int64
	Published time.Time
}

// source returns how the item's torrent is added: a magnet link or an
// http(s) URL, and whether it has one.
func (it *feedItem) source() (magnet, link string, ok bool) {
	switch {
	case it.Magnet != "":
		return it.Magnet, "", true
	case it.TorrentURL != "":
		return "", it.TorrentURL, true
	case it.Link != "":
		return "", it.Link, true
	}
	return "", "", false
}

// parsedFeed is a feed read.
type parsedFeed struct {
	Title string
	Items []feedItem
	// Truncated says items past max were left out.
	Truncated bool
}

// The elements of an item, by their local names in any namespace.
type xmlLink struct {
	Rel    string `xml:"rel,attr"`
	Href   string `xml:"href,attr"`
	Type   string `xml:"type,attr"`
	Length string `xml:"length,attr"`
	Text   string `xml:",chardata"`
}

type xmlEnclosure struct {
	URL    string `xml:"url,attr"`
	Length string `xml:"length,attr"`
	Type   string `xml:"type,attr"`
}

type xmlTorrent struct {
	MagnetURI     string `xml:"magnetURI"`
	ContentLength string `xml:"contentLength"`
	InfoHash      string `xml:"infoHash"`
}

type xmlItem struct {
	Title      string         `xml:"title"`
	GUID       string         `xml:"guid"`
	ID         string         `xml:"id"`
	Links      []xmlLink      `xml:"link"`
	Enclosures []xmlEnclosure `xml:"enclosure"`
	PubDate    string         `xml:"pubDate"`
	Date       string         `xml:"date"`
	Published  string         `xml:"published"`
	Updated    string         `xml:"updated"`
	// The torrent namespace (ezRSS), nested or flat, and a site's own.
	Torrent       xmlTorrent `xml:"torrent"`
	MagnetURI     string     `xml:"magnetURI"`
	ContentLength string     `xml:"contentLength"`
	InfoHash      string     `xml:"infoHash"`
	Size          string     `xml:"size"`
}

// parseFeed reads body, keeping at most max items.
func parseFeed(body []byte, max int) (*parsedFeed, error) {
	d := xml.NewDecoder(bytes.NewReader(body))
	d.Strict = false
	d.Entity = xml.HTMLEntity
	d.CharsetReader = charsetReader
	out := &parsedFeed{}
	var stack []string
	root := ""
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrFeed, err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			name := t.Name.Local
			if root == "" {
				root = name
				switch name {
				case "rss", "RDF", "feed":
				default:
					return nil, fmt.Errorf("%w: <%s> is not RSS or Atom", ErrFeed, name)
				}
			}
			parent := ""
			if len(stack) > 0 {
				parent = stack[len(stack)-1]
			}
			switch {
			case name == "item" && root != "feed" || name == "entry" && root == "feed":
				if len(out.Items) >= max {
					out.Truncated = true
					if err := d.Skip(); err != nil {
						return nil, fmt.Errorf("%w: %v", ErrFeed, err)
					}
					continue
				}
				var x xmlItem
				if err := d.DecodeElement(&x, &t); err != nil {
					return nil, fmt.Errorf("%w: item %d: %v", ErrFeed, len(out.Items)+1, err)
				}
				if it, ok := x.item(root == "feed"); ok {
					out.Items = append(out.Items, it)
				}
				continue
			case name == "title" && out.Title == "" && (parent == "channel" || parent == "feed"):
				var s string
				if err := d.DecodeElement(&s, &t); err != nil {
					return nil, fmt.Errorf("%w: %v", ErrFeed, err)
				}
				out.Title = clip(strings.TrimSpace(s), maxRSSTitle)
				continue
			}
			stack = append(stack, name)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	if root == "" {
		return nil, fmt.Errorf("%w: no document", ErrFeed)
	}
	return out, nil
}

// charsetReader reads the charsets a feed may be in without a dependency:
// UTF-8 and ASCII as they are, ISO-8859-1 decoded.
func charsetReader(charset string, in io.Reader) (io.Reader, error) {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "", "utf-8", "utf8", "us-ascii", "ascii":
		return in, nil
	case "iso-8859-1", "iso8859-1", "latin1", "latin-1", "l1":
		data, err := io.ReadAll(in)
		if err != nil {
			return nil, err
		}
		var b strings.Builder
		b.Grow(len(data))
		for _, c := range data {
			b.WriteRune(rune(c))
		}
		return strings.NewReader(b.String()), nil
	}
	return nil, fmt.Errorf("the charset %q is not read: UTF-8 and ISO-8859-1 are", charset)
}

// item makes x a feedItem, or reports it has nothing to add.
func (x *xmlItem) item(atom bool) (feedItem, bool) {
	it := feedItem{Title: clip(strings.TrimSpace(x.Title), maxRSSTitle)}
	magnet := firstOf(x.Torrent.MagnetURI, x.MagnetURI)
	var page string
	for _, l := range x.Links {
		href := strings.TrimSpace(l.Href)
		if !atom && href == "" {
			href = strings.TrimSpace(l.Text)
		}
		switch strings.ToLower(l.Rel) {
		case "enclosure":
			x.Enclosures = append(x.Enclosures, xmlEnclosure{URL: href, Length: l.Length, Type: l.Type})
		case "", "alternate":
			if page == "" {
				page = href
			}
		}
	}
	var encLen int64
	for _, e := range x.Enclosures {
		u := strings.TrimSpace(e.URL)
		switch {
		case isMagnet(u):
			magnet = firstOf(magnet, u)
		case isHTTP(u) && it.TorrentURL == "":
			it.TorrentURL = u
			encLen, _ = strconv.ParseInt(strings.TrimSpace(e.Length), 10, 64)
		}
	}
	if isMagnet(page) {
		magnet = firstOf(magnet, page)
	} else if isHTTP(page) {
		it.Link = page
	}
	if magnet == "" && it.TorrentURL == "" {
		// An info hash alone, made a magnet link, rather than a page.
		if h := strings.TrimSpace(firstOf(x.Torrent.InfoHash, x.InfoHash)); isInfoHash(h) {
			magnet = "magnet:?xt=urn:btih:" + strings.ToLower(h)
			if it.Title != "" {
				magnet += "&dn=" + url.QueryEscape(it.Title)
			}
		}
	}
	if isMagnet(magnet) && len(magnet) <= MaxMagnetBytes {
		it.Magnet = magnet
	}
	if len(it.TorrentURL) > maxURLBytes {
		it.TorrentURL = ""
	}
	if len(it.Link) > maxURLBytes {
		it.Link = ""
	}
	// The content's size: the torrent namespace's, a site's own size,
	// else the enclosure's length.
	if n, err := strconv.ParseInt(strings.TrimSpace(firstOf(x.Torrent.ContentLength, x.ContentLength)), 10, 64); err == nil && n > 0 {
		it.Size = n
	} else if n := parseSize(x.Size); n > 0 {
		it.Size = n
	} else if encLen > 0 {
		it.Size = encLen
	}
	for _, s := range []string{x.PubDate, x.Published, x.Updated, x.Date} {
		if t, ok := parseFeedTime(s); ok {
			it.Published = t
			break
		}
	}
	key := strings.TrimSpace(firstOf(x.GUID, x.ID))
	if key == "" {
		key = firstOf(page, it.TorrentURL, it.Magnet)
	}
	if key == "" && it.Title != "" {
		key = "title:" + it.Title
	}
	if key == "" {
		return it, false
	}
	if len(key) > maxRSSKey || !utf8.ValidString(key) {
		sum := sha256.Sum256([]byte(key))
		key = "sha256:" + hex.EncodeToString(sum[:])
	}
	it.Key = key
	return it, true
}

func firstOf(s ...string) string {
	for _, v := range s {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

func isMagnet(s string) bool { return len(s) > 8 && strings.EqualFold(s[:8], "magnet:?") }

func isHTTP(s string) bool {
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func isInfoHash(s string) bool {
	if len(s) != 40 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

// clip cuts s to at most n bytes, on a character's boundary.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// parseSize reads a size as a site writes it: bytes, or a number and a
// unit, as "1.5 GiB" or "700 MB" (taken as binary multiples alike).
func parseSize(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	i := strings.IndexFunc(s, func(r rune) bool { return (r < '0' || r > '9') && r != '.' })
	num, unit := s, ""
	if i >= 0 {
		num, unit = s[:i], strings.ToLower(strings.TrimSpace(s[i:]))
	}
	v, err := strconv.ParseFloat(num, 64)
	if err != nil || v <= 0 {
		return 0
	}
	mult := 1.0
	switch strings.TrimSuffix(strings.TrimSuffix(unit, "b"), "i") {
	case "":
	case "k":
		mult = 1 << 10
	case "m":
		mult = 1 << 20
	case "g":
		mult = 1 << 30
	case "t":
		mult = 1 << 40
	default:
		return 0
	}
	if v*mult > 1<<62 {
		return 0
	}
	return int64(v * mult)
}

// parseFeedTime reads the times RSS (RFC 822, loosely) and Atom (RFC 3339)
// write.
func parseFeedTime(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC1123Z, time.RFC1123, "Mon, 2 Jan 2006 15:04:05 -0700",
		"Mon, 2 Jan 2006 15:04:05 MST", "2 Jan 2006 15:04:05 -0700", time.RFC822Z, time.RFC822, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}
