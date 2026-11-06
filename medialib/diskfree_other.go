//go:build !linux && !darwin && !freebsd && !windows

package medialib

// diskFree cannot tell the space free here: min_free_bytes is not checked.
func diskFree(string) (uint64, bool) { return 0, false }
