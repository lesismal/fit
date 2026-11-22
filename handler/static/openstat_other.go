//go:build !linux

package static

import "io/fs"

// openStat opens nothing here: ok false hands every lookup to statLink.
func openStat(string) (fs.FileInfo, sendable, bool, error) { return nil, nil, false, nil }
