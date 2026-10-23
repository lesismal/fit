//go:build !linux && !windows && !darwin && !dragonfly && !freebsd && !netbsd && !openbsd

package sysmem

func total() uint64 { return 0 }
