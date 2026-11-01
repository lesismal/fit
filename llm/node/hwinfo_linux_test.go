package node

import "testing"

func TestReadTrimMissing(t *testing.T) {
	if got := readTrim("/nonexistent/fit-llm-node"); got != "" {
		t.Fatalf("readTrim of a missing file = %q", got)
	}
}
