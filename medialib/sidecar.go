package medialib

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"
)

// Sidecar is an item's <name>.json: its ID, title and poster, and the files
// of its renditions, audio and subtitles, by their paths relative to the
// sidecar's directory, which they may not lead out of.
//
//	{
//	  "id": "sintel",
//	  "title": "Sintel",
//	  "poster": "art/sintel.jpg",
//	  "renditions": [{"file": "sintel-1080.mp4", "name": "1080p"}, {"file": "sintel-720.mp4", "name": "720p"}],
//	  "audio": [{"file": "sintel.fr.m4a", "name": "Français", "language": "fr"}],
//	  "subtitles": [{"file": "subs/sintel.zh.srt", "name": "中文", "language": "zh", "default": true}]
//	}
type Sidecar struct {
	ID         string            `json:"id,omitempty"`
	Title      string            `json:"title,omitempty"`
	Poster     string            `json:"poster,omitempty"`
	Renditions []SidecarFile     `json:"renditions,omitempty"`
	Audio      []SidecarFile     `json:"audio,omitempty"`
	Subtitles  []SidecarSubtitle `json:"subtitles,omitempty"`
}

// SidecarFile is a rendition or an audio file a sidecar names.
type SidecarFile struct {
	File     string `json:"file"`
	Name     string `json:"name,omitempty"`
	Language string `json:"language,omitempty"`
	Default  bool   `json:"default,omitempty"`
}

// SidecarSubtitle is a subtitle a sidecar names.
type SidecarSubtitle struct {
	File     string `json:"file"`
	Name     string `json:"name,omitempty"`
	Language string `json:"language,omitempty"`
	Default  bool   `json:"default,omitempty"`
	Forced   bool   `json:"forced,omitempty"`
}

// maxSidecarText is the longest a sidecar's title, name or language may be.
const maxSidecarText = 1024

// readSidecar reads and parses the sidecar e, whose size the scan bounds by
// max_sidecar_bytes. It blocks: it runs on the library's pool.
func readSidecar(e *entry, follow bool) (*Sidecar, error) {
	file := e.file()
	f, err := openFile(&file, follow)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// The scan left out a sidecar larger than limit, and its size is
	// checked as it is opened.
	data := make([]byte, f.Size)
	if _, err := f.ReadAt(data, 0); err != nil {
		return nil, err
	}
	return parseSidecar(data)
}

// parseSidecar parses a sidecar strictly: an unknown field, or a text that
// is not UTF-8 or is too long, is an error.
func parseSidecar(data []byte) (*Sidecar, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var sd Sidecar
	if err := dec.Decode(&sd); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, errors.New("more than one JSON value")
	}
	if sd.ID != "" && !ValidID(sd.ID) {
		return nil, fmt.Errorf("id %q is not an item's ID: parts of 1 to 128 letters, digits, '.', '_', '~' and '-', not starting with '.', joined by '/'", sd.ID)
	}
	texts := []string{sd.Title}
	for _, list := range [][]SidecarFile{sd.Renditions, sd.Audio} {
		for _, m := range list {
			texts = append(texts, m.Name, m.Language)
		}
	}
	for _, s := range sd.Subtitles {
		texts = append(texts, s.Name, s.Language)
	}
	for _, t := range texts {
		if len(t) > maxSidecarText || !utf8.ValidString(t) {
			return nil, fmt.Errorf("a title, name or language is not UTF-8, or longer than %d bytes", maxSidecarText)
		}
	}
	return &sd, nil
}
