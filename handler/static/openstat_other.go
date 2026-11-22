//go:build !linux

package static

import "io/fs"

// openStat opens nothing here: ok false hands every lookup to statLink.
func openStat(string, *lookupSpare) (fs.FileInfo, sendable, bool, error) {
	return nil, nil, false, nil
}

// lookupSpare is openStat's room, which it needs none of here.
type lookupSpare struct{}

func newLookup() (*lookup, *lookupSpare) { return &lookup{}, nil }
