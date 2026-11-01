package login

import "testing"

// TestEndUnknown checks ending a session that is not there reports none.
func TestEndUnknown(t *testing.T) {
	l, _ := newLogins(t)
	if s, ok := l.End("no such token"); ok || s != (Session{}) {
		t.Errorf("End of an unknown token: %+v %v", s, ok)
	}
}
