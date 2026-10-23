package limit

import "testing"

// TestPeerAndName checks peer of an address with a port and of a bare one,
// and validName of names with and without characters a token may not hold.
func TestPeerAndName(t *testing.T) {
	for s, want := range map[string]string{
		"[::ffff:192.0.2.1]:80": "192.0.2.1", "192.0.2.1:80": "192.0.2.1", "192.0.2.1": "192.0.2.1",
		"fe80::1%eth0": "fe80::1", "": "invalid IP",
	} {
		if got := peer(s); got.String() != want {
			t.Errorf("peer(%q) = %v, want %s", s, got, want)
		}
	}
	for name, want := range map[string]bool{"": false, "X-Api_Key.1~": true, "x y": false, "a:b": false} {
		if got := validName(name); got != want {
			t.Errorf("validName(%q) = %v", name, got)
		}
	}
}
