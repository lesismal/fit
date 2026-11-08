//go:build race

package fitsync

// raceEnabled says the tests run under the race detector, which slows them
// some tenfold.
const raceEnabled = true
