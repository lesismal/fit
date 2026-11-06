package channels

import "github.com/lesismal/fit/config"

// Encryption is how a channel's HLS is encrypted (encrypt, I136): with the
// keys of the channel's library (medialib.Library.Keys), a key every
// Rotation segments. The hls handler encrypts the segments and serves the
// keys; the channel itself publishes its packets clear, as every other
// egress plays them.
type Encryption struct {
	// Library is the media library whose secret the keys derive from.
	Library string
	// Rotation is how many segments one key encrypts.
	Rotation int
}

// encryptions returns how the channels of chs that encrypt their HLS do it,
// by name, nil for none.
func encryptions(chs []config.StreamChannel) map[string]Encryption {
	var out map[string]Encryption
	for i := range chs {
		c := &chs[i]
		if !c.Encrypt {
			continue
		}
		if out == nil {
			out = map[string]Encryption{}
		}
		out[c.Name] = Encryption{Library: c.Library, Rotation: c.Rotation()}
	}
	return out
}

// Encryption returns how the HLS of stream is encrypted, and false when
// stream is not a channel of the manager that encrypts it. It takes no lock,
// for every request of the hls handler; a nil manager has none.
func (m *Manager) Encryption(stream string) (Encryption, bool) {
	if m == nil {
		return Encryption{}, false
	}
	p := m.enc.Load()
	if p == nil {
		return Encryption{}, false
	}
	e, ok := (*p)[stream]
	return e, ok
}
