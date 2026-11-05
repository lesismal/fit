//go:build !windows

package dotfile

import "os"

// named reports whether dir has an entry written name. No call of the
// system's names the entry a lookup found, so dir's names are read until
// name is among them.
func named(dir, name string) bool {
	f, err := os.Open(dir)
	if err != nil {
		return false
	}
	defer f.Close()
	for {
		names, err := f.Readdirnames(256)
		for _, n := range names {
			if n == name {
				return true
			}
		}
		if err != nil {
			return false
		}
	}
}
