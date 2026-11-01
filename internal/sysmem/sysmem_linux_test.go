package sysmem

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCgroupLimit checks the cgroup's limit is read where it is a number,
// and taken where it is below the machine's memory; "max", a file that is
// not there, and anything else are no limit.
func TestCgroupLimit(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		return path
	}
	low, unlimited, junk := write("low", "1048576\n"), write("max", "max\n"), write("junk", "")
	for path, want := range map[string]uint64{low: 1 << 20, unlimited: 0, junk: 0, filepath.Join(dir, "none"): 0} {
		if got := readLimit(path); got != want {
			t.Errorf("readLimit(%s) = %d, want %d", filepath.Base(path), got, want)
		}
	}
	saved := cgroupLimits
	defer func() { cgroupLimits = saved }()
	cgroupLimits = []string{unlimited, low}
	if got := total(); got != 1<<20 {
		t.Errorf("total() = %d under a 1 MiB limit", got)
	}
}
