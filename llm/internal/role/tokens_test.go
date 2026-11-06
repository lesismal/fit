package role

import (
	"strings"
	"testing"
)

// A binding is written into the token's members; one the member cannot take,
// as tokens that do not decode, is logged and changes nothing.
func TestTokenBind(t *testing.T) {
	ed := &editor{cur: []byte(`{"tokens": [{"id": "0123456789abcdef", "name": "a", "hash": "` + strings.Repeat("ab", 32) + `"}]}`)}
	b := newBase(t, "hub", ed)
	b.bind("0123456789abcdef", "node-1")
	<-ed.done
	ts, _ := ed.member()["tokens"].([]any)
	if len(ts) != 1 || !strings.Contains(stringOf(ts[0]), "node-1") {
		t.Fatalf("tokens %v", ts)
	}
	ed.mu.Lock()
	ed.cur = []byte(`{"tokens": 5}`)
	ed.mu.Unlock()
	b.bind("0123456789abcdef", "node-2")
	<-ed.done
	if got := string(ed.cur); got != `{"tokens": 5}` {
		t.Fatalf("member %s", got)
	}
	if b.LinkTokens() == nil {
		t.Fatal("no manager")
	}
}

func stringOf(v any) string {
	m, _ := v.(map[string]any)
	var b strings.Builder
	for _, x := range m["members"].([]any) {
		b.WriteString(x.(string))
	}
	return b.String()
}
