package medialib

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
)

// Subtitle reads the subtitle s on the library's pool, as WebVTT, an SRT
// file turned into it, and calls fn with it there, or with the error:
// ErrNotServed for one listed with an error, ErrChanged for one not as the
// catalog has it, ErrBusy, ErrRemoved, or what makes it neither WebVTT nor
// SRT. fn is called once, at once on the caller's goroutine when the pool
// refuses the read.
func (l *Library) Subtitle(s *Subtitle, fn func([]byte, error)) {
	if s.Error != "" {
		fn(nil, ErrNotServed)
		return
	}
	file, format := s.File, s.Format
	l.async(func() {
		h, err := l.OpenFile(&file)
		if err != nil {
			fn(nil, err)
			return
		}
		data, err := io.ReadAll(io.NewSectionReader(h.f, 0, h.Size))
		h.Close()
		if err == nil {
			data, err = ToWebVTT(format, data)
		}
		fn(data, err)
	}, func(err error) { fn(nil, err) })
}

var bom = []byte("\xef\xbb\xbf")

// ToWebVTT returns data, a subtitle file of format vtt or srt, as WebVTT:
// a WebVTT file as it is, without a byte order mark, and an SRT file
// turned into one, its times written as WebVTT writes them and what SRT
// adds after them left out.
func ToWebVTT(format string, data []byte) ([]byte, error) {
	data = bytes.TrimPrefix(data, bom)
	switch format {
	case "vtt":
		if !bytes.HasPrefix(data, []byte("WEBVTT")) || len(data) > 6 && !bytes.ContainsAny(data[6:7], " \t\r\n") {
			return nil, errors.New("not a WebVTT file: it does not start with WEBVTT")
		}
		return data, nil
	case "srt":
		return srtToVTT(data)
	}
	return nil, fmt.Errorf("subtitles of format %q", format)
}

// srtTime is a time of an SRT cue: hours, minutes, seconds and
// milliseconds, the last after a comma, or a dot as some write it.
var srtTime = regexp.MustCompile(`^\s*([0-9]{1,3}):([0-9]{1,2}):([0-9]{1,2})[,.]([0-9]{1,3})\s*-->\s*([0-9]{1,3}):([0-9]{1,2}):([0-9]{1,2})[,.]([0-9]{1,3})`)

func srtToVTT(data []byte) ([]byte, error) {
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	data = bytes.ReplaceAll(data, []byte("\r"), []byte("\n"))
	out := bytes.NewBuffer(make([]byte, 0, len(data)+len(data)/8+16))
	out.WriteString("WEBVTT\n\n")
	cues := 0
	for line := range bytes.Lines(data) {
		line = bytes.TrimSuffix(line, []byte("\n"))
		if m := srtTime.FindSubmatch(line); m != nil {
			cues++
			fmt.Fprintf(out, "%s --> %s\n", vttTime(m[1:5]), vttTime(m[5:9]))
			continue
		}
		if cues == 0 {
			// The first cue's number, and what is before it.
			continue
		}
		// A cue's text may not hold -->, which would start a cue.
		out.Write(bytes.ReplaceAll(line, []byte("-->"), []byte("--&gt;")))
		out.WriteByte('\n')
	}
	if cues == 0 {
		return nil, errors.New("not an SRT file: it has no cue")
	}
	return out.Bytes(), nil
}

// vttTime writes an SRT time's parts as WebVTT does: hh:mm:ss.ttt.
func vttTime(p [][]byte) string {
	n := make([]int, 4)
	for i := range p {
		n[i], _ = strconv.Atoi(string(p[i]))
	}
	// Milliseconds written with fewer than three digits are tenths or
	// hundredths.
	for range 3 - len(p[3]) {
		n[3] *= 10
	}
	return fmt.Sprintf("%02d:%02d:%02d.%03d", n[0], n[1], n[2], n[3])
}
