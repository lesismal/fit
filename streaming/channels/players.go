package channels

// Subscribers returns how many subscribers the hub has of stream: players
// over RTMP, RTSP, SRT, HTTP-FLV and WHEP, push targets, and a segmenter
// for each HLS format played, which serves every HLS player; 0 for a
// stream the hub does not have, or a nil Manager.
func (m *Manager) Subscribers(stream string) int {
	if m == nil || m.o.Hub == nil {
		return 0
	}
	info, ok := m.o.Hub.Stream(stream)
	if !ok {
		return 0
	}
	return info.Subscribers
}
