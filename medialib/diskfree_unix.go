//go:build linux || darwin || freebsd

package medialib

import "golang.org/x/sys/unix"

// diskFree returns the bytes free to fit on the disk of dir, or false when
// it cannot be told.
func diskFree(dir string) (uint64, bool) {
	var st unix.Statfs_t
	if unix.Statfs(dir, &st) != nil {
		return 0, false
	}
	return uint64(st.Bavail) * uint64(st.Bsize), true
}

// DiskFree returns the bytes free to fit on the disk of dir, or false when
// it cannot be told: what the BitTorrent client checks before and while it
// downloads.
func DiskFree(dir string) (uint64, bool) { return diskFree(dir) }
