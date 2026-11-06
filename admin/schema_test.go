package admin

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"slices"
	"testing"

	"github.com/lesismal/fit/config"
)

// GET /schema answers the config's JSON Schema, whose digest GET /node
// gives, and /node lists it among the capabilities: a manager's node
// switcher reads a member's schema there when it reaches its Admin API.
func TestSchema(t *testing.T) {
	f := start(t, doc("one"), &config.Admin{})
	resp, body := f.get(t, "/schema")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "application/schema+json" || !json.Valid([]byte(body)) {
		t.Fatalf("GET /schema: %d %v %.100s", resp.StatusCode, resp.Header, body)
	}
	sum := sha256.Sum256([]byte(body))
	_, nb := f.get(t, "/node")
	var n nodeAnswer
	if err := json.Unmarshal([]byte(nb), &n); err != nil {
		t.Fatal(err)
	}
	if n.SchemaDigest != "sha256:"+hex.EncodeToString(sum[:]) || !slices.Contains(n.Capabilities, CapSchema) {
		t.Fatalf("%s %q", n.SchemaDigest, n.Capabilities)
	}
}
