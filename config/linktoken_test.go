package config

import (
	"strings"
	"testing"
)

// tok is a well-formed link token, for the checks of a dialer's token.
const tok = LinkTokenPrefix + "0123456789abcdef.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func TestLinkTokenChecks(t *testing.T) {
	if CheckLinkTokenString(tok) != "" {
		t.Fatal("a good token refused")
	}
	if !ValidLinkTokenID("0123456789abcdef") || ValidLinkTokenID("0123456789ABCDEF") || ValidLinkTokenID("0123") || ValidLinkTokenID("0123456789abcdeg") {
		t.Fatal("ValidLinkTokenID")
	}
	lt := LinkToken{ExpiresAt: 10}
	if lt.Expired(9) || !lt.Expired(10) || (&LinkToken{}).Expired(1<<40) {
		t.Fatal("Expired")
	}
	hash := strings.Repeat("ab", 32)
	many := make([]string, MaxLinkTokens+1)
	for i := range many {
		many[i] = `{"id": "0123456789abcdef", "name": "a", "hash": "` + hash + `"}`
	}
	members := make([]string, MaxLinkTokenMembers+1)
	for i := range members {
		members[i] = `"m` + strings.Repeat("x", i) + `"`
	}
	for _, bad := range []struct{ doc, want string }{
		{`{"tunnel": {"hub": {"tokens": [` + strings.Join(many, ",") + `]}}}`, "at most 1024 tokens"},
		{`{"tunnel": {"hub": {"tokens": [{"id": "0123456789abcdef", "name": "a", "hash": "` + hash + `", "members": [` + strings.Join(members, ",") + `]}]}}}`, "at most 256 members"},
		{`{"tunnel": {"hub": {"tokens": [{"id": "0123456789abcdef", "name": "a", "hash": "` + hash + `", "expires_at": -1}]}}}`, "less than the minimum"},
	} {
		_, err := Load([]byte(bad.doc))
		if err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%.100s: %v, want %q", bad.doc, err, bad.want)
		}
	}
	good := `{"tunnel": {"hub": {"tokens": [{"id": "0123456789abcdef", "name": "lab", "hash": "` + hash + `", "members": ["node/lab one"]}]},
		"node": {"hubs": [{"endpoints": [{"address": "127.0.0.1:1"}], "token": "` + tok + `"}]}},
		"llm": {"hub": {"listen": {"address": "127.0.0.1:9200"}, "peers": [{"address": "127.0.0.1:1", "token": "` + tok + `", "role": "node"}]}}}`
	if _, err := Load([]byte(good)); err != nil {
		t.Fatal(err)
	}
}
