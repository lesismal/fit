package mcp

import (
	"errors"
	"strings"

	"github.com/lesismal/fit/assistant"
)

// docsURI is the config's reference; a chapter is under it, by the anchor
// of its title.
const docsURI = "fit://docs/reference"

// chapter is the reference's part under a title of its own: the whole, the
// first, then each "## " section.
type chapter struct {
	slug, title, text string
}

// reference returns the reference, whole and by chapter, made once. It may
// take a while: on the pool.
func (s *Server) reference() ([]chapter, error) {
	s.docsOnce.Do(func() {
		if s.o.Docs == nil {
			return
		}
		data, err := s.o.Docs()
		if err != nil {
			s.docsErr = err
			return
		}
		s.docs = splitChapters(string(data))
	})
	return s.docs, s.docsErr
}

// splitChapters returns md whole, and its "## " sections, each with the
// anchor of its title.
func splitChapters(md string) []chapter {
	out := []chapter{{title: "The config's reference", text: md}}
	lines := strings.SplitAfter(md, "\n")
	cur := -1
	inFence := false
	for _, l := range lines {
		if strings.HasPrefix(l, "```") {
			inFence = !inFence
		}
		if !inFence && strings.HasPrefix(l, "## ") {
			title := strings.TrimSpace(l[3:])
			out = append(out, chapter{slug: anchor(title), title: title})
			cur = len(out) - 1
		}
		if cur > 0 {
			out[cur].text += l
		}
	}
	return out
}

// anchor is a title's anchor as GitHub makes it: lower case, spaces as '-',
// and only letters, digits, '-' and '_' kept.
func anchor(title string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(title) {
		switch {
		case r == ' ':
			b.WriteByte('-')
		case r == '-' || r == '_' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			b.WriteRune(r)
		}
	}
	return b.String()
}

// readDocs returns the reference, or a chapter of it, uri naming which.
func (s *Server) readDocs(uri string) (string, error) {
	docs, err := s.reference()
	if err != nil {
		return "", errors.New("the reference could not be made: " + err.Error())
	}
	if len(docs) == 0 {
		return "", assistant.ErrNoResource
	}
	if uri == docsURI {
		return clipText(docs[0].text), nil
	}
	slug, ok := strings.CutPrefix(uri, docsURI+"/")
	if ok {
		for _, c := range docs[1:] {
			if c.slug == slug {
				return clipText(c.text), nil
			}
		}
	}
	return "", assistant.ErrNoResource
}

// clipText cuts text to assistant.MaxResourceBytes, and says so.
func clipText(text string) string {
	if len(text) <= assistant.MaxResourceBytes {
		return text
	}
	n := assistant.MaxResourceBytes
	for n > 0 && text[n]&0xc0 == 0x80 {
		n--
	}
	return text[:n] + "\n[cut: the rest is past the resource's bound; read a chapter instead]"
}
