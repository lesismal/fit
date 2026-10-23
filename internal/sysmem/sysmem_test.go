package sysmem

import (
	"runtime"
	"testing"
)

func TestTotal(t *testing.T) {
	switch runtime.GOOS {
	case "linux", "darwin", "freebsd", "windows":
	default:
		t.Skipf("no memory reading tested on %s", runtime.GOOS)
	}
	n := Total()
	if n < 64<<20 {
		t.Fatalf("Total() = %d, want the machine's memory", n)
	}
	if Total() != n {
		t.Fatal("Total() changed between calls")
	}
}
