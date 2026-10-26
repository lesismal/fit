package config

import "testing"

// One connection's send queue is capped by default, and a config may move
// the cap.
func TestLimitsQueueBytes(t *testing.T) {
	var l Limits
	if got := l.QueueBytes(); got != DefaultMaxWriteQueueBytes {
		t.Errorf("default QueueBytes = %d, want %d", got, DefaultMaxWriteQueueBytes)
	}
	l.MaxWriteQueueBytes = 1 << 20
	if got := l.QueueBytes(); got != 1<<20 {
		t.Errorf("set QueueBytes = %d, want %d", got, 1<<20)
	}
}
