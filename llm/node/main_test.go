package node

import (
	"os"
	"testing"
)

// TestMain gives the fake tools of the tests, scripts just written, time to
// start on a loaded machine: macOS may take seconds to run a new one.
func TestMain(m *testing.M) {
	cmdTimeoutScale = 6
	os.Exit(m.Run())
}
