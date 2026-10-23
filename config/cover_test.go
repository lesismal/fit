package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestPositionPastTheEnd checks that an offset past the source's end is
// placed at its end, and that a decoder error of no known kind is placed
// where the decoder stopped: what the parser's own errors never reach.
func TestPositionPastTheEnd(t *testing.T) {
	s := &source{data: []byte("{\n  \"a\": 1\n}")}
	if line, col := s.pos(100); line != 3 || col != 2 {
		t.Errorf("past the end: %d:%d, want 3:2", line, col)
	}
	data := []byte(`{"a": 1}`)
	p := &parser{src: &source{data: data}, dec: json.NewDecoder(bytes.NewReader(data))}
	err := p.syntaxError(errors.New("broken"))
	if err.Line != 1 || err.Col != 1 || !strings.Contains(err.Msg, "invalid JSON: broken") {
		t.Errorf("%+v", err)
	}
}
