package config

import (
	"reflect"
	"time"
)

// Duration is a length of time, written in a config as a string that
// time.ParseDuration takes, such as "10s", "1m30s" or "250ms". It may not be
// negative.
type Duration time.Duration

// Std returns d as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// String returns d as time.Duration writes it.
func (d Duration) String() string { return time.Duration(d).String() }

// DurationType is Duration's type, which the decoder and the schema treat as
// a string.
var DurationType = reflect.TypeFor[Duration]()

func (d *decoder) decodeDuration(n *node, path string, rv reflect.Value) {
	if !d.expect(n, path, kindString) {
		return
	}
	v, err := time.ParseDuration(n.s)
	if err != nil {
		d.errorf(n, n.off, path, `%q is not a duration, such as "10s" or "1m30s"`, n.s)
		return
	}
	if v < 0 {
		d.errorf(n, n.off, path, "%q is negative", n.s)
		return
	}
	rv.SetInt(int64(v))
}
