package main

import (
	"errors"
	"syscall"
	"testing"
)

// failingRawConn is a socket whose descriptor cannot be reached.
type failingRawConn struct{}

func (failingRawConn) Control(func(uintptr)) error    { return errors.New("closed") }
func (failingRawConn) Read(func(uintptr) bool) error  { return errors.New("closed") }
func (failingRawConn) Write(func(uintptr) bool) error { return errors.New("closed") }

var _ syscall.RawConn = failingRawConn{}

func TestBindAddressNoPortFails(t *testing.T) {
	if err := bindAddressNoPort("tcp4", "127.0.0.1:0", failingRawConn{}); err == nil {
		t.Error("no error from a socket out of reach")
	}
}
