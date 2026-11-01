package stun

import (
	"errors"
	"strings"
	"testing"
)

// TestAttrBounds checks the attribute readers refuse values of the wrong
// size, and that a reason phrase past its bound is cut.
func TestAttrBounds(t *testing.T) {
	var m Message
	m.Build(Type{MethodBinding, ClassSuccess}, NewTransactionID())
	m.Add(AttrXORMappedAddress, []byte{0, 1})
	m.Add(AttrUnknownAttributes, []byte{0, 1, 2})
	m.AddUint64(AttrICEControlled, 7)
	m.Add(AttrICEControlling, []byte{1, 2, 3, 4})
	m.Add(AttrMessageIntegrity, []byte{1, 2, 3, 4})
	if _, err := m.XORAddr(AttrXORMappedAddress); !errors.Is(err, ErrBadAttr) {
		t.Errorf("a short address: %v", err)
	}
	if _, err := m.UnknownAttributes(nil); !errors.Is(err, ErrBadAttr) {
		t.Errorf("an odd UNKNOWN-ATTRIBUTES: %v", err)
	}
	if _, err := m.Uint32(AttrLifetime); !errors.Is(err, ErrNoAttr) {
		t.Errorf("no LIFETIME: %v", err)
	}
	if v, err := m.Uint64(AttrICEControlled); err != nil || v != 7 {
		t.Errorf("ICE-CONTROLLED %d %v", v, err)
	}
	if _, err := m.Uint64(AttrICEControlling); !errors.Is(err, ErrBadAttr) {
		t.Errorf("a short ICE-CONTROLLING: %v", err)
	}
	if err := m.CheckIntegrity([]byte("key")); !errors.Is(err, ErrBadAttr) {
		t.Errorf("a short MESSAGE-INTEGRITY: %v", err)
	}

	var e Message
	e.Build(Type{MethodBinding, ClassError}, NewTransactionID())
	e.AddErrorCode(CodeBadRequest, strings.Repeat("r", maxReason+10))
	if code, reason, err := e.ErrorCode(); err != nil || code != CodeBadRequest || len(reason) != maxReason {
		t.Errorf("ERROR-CODE %d %d %v", code, len(reason), err)
	}
}
