package medialib

import "golang.org/x/sys/windows"

// diskFree returns the bytes free to fit on the disk of dir, or false when
// it cannot be told.
func diskFree(dir string) (uint64, bool) {
	p, err := windows.UTF16PtrFromString(dir)
	if err != nil {
		return 0, false
	}
	var free uint64
	if windows.GetDiskFreeSpaceEx(p, &free, nil, nil) != nil {
		return 0, false
	}
	return free, true
}

// DiskFree returns the bytes free to fit on the disk of dir, or false when
// it cannot be told: what the BitTorrent client checks before and while it
// downloads.
func DiskFree(dir string) (uint64, bool) { return diskFree(dir) }
