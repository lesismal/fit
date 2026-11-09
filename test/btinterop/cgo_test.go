//go:build cgo

package btinterop

// haveLibutp says anacrolix speaks uTP through go-libutp, the reference
// libutp, which needs cgo; without it the uTP cases against anacrolix are
// skipped.
const haveLibutp = true
