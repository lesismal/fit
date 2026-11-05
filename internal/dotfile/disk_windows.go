package dotfile

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

// named reports whether dir has an entry written name: FindFirstFile finds
// the entry Windows opens for name, and gives its own name, its long one
// for a short name, in the case it is written in.
func named(dir, name string) bool {
	p, err := windows.UTF16PtrFromString(filepath.Join(dir, name))
	if err != nil {
		return false
	}
	var data windows.Win32finddata
	h, err := windows.FindFirstFile(p, &data)
	if err != nil {
		return false
	}
	_ = windows.FindClose(h)
	return windows.UTF16ToString(data.FileName[:]) == name
}
