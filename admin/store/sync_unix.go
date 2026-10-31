//go:build !windows

package store

import "os"

// syncDir syncs the directory dir, so that a rename in it lasts a crash.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}
