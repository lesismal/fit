package app

import (
	"context"
	"testing"
	"time"
)

func TestDrainTimeout(t *testing.T) {
	if d := (&Host{}).DrainTimeout(); d != DefaultDrainTimeout {
		t.Fatalf("without a Manager: %s", d)
	}
	m := NewManager(nil, nil, nil, nil)
	defer m.Stop(context.Background())
	h := &Host{m: m}
	if d := h.DrainTimeout(); d != DefaultDrainTimeout {
		t.Fatalf("by default: %s", d)
	}
	m.SetDrainTimeout(5 * time.Second)
	if d := h.DrainTimeout(); d != 5*time.Second {
		t.Fatalf("set: %s", d)
	}
}
