package l4

import (
	"encoding/binary"
	"errors"
	"strings"
)

// MaxClientHello bounds the bytes a tcp listener that routes by SNI reads
// for a ClientHello: one with large key shares spans several records, but
// none in use comes near it.
const MaxClientHello = 64 << 10

var errNotClientHello = errors.New("l4: not a TLS ClientHello")

// readSNI reads the server name a TLS ClientHello, which data begins with,
// asks for, in lower case, or "" for a hello that names none. done is false,
// with no error, while more data is needed. The hello may span records,
// which is why it is gathered from them first.
func readSNI(data []byte) (name string, done bool, err error) {
	var hello []byte
	need := -1 // the handshake message's length, once its header is in
	for len(data) > 0 {
		if len(data) < 5 {
			return "", false, nil
		}
		if data[0] != 0x16 || data[1] != 3 {
			return "", false, errNotClientHello
		}
		size := int(binary.BigEndian.Uint16(data[3:5]))
		if size == 0 || size > 1<<14 {
			return "", false, errNotClientHello
		}
		if len(data) < 5+size {
			return "", false, nil
		}
		hello = append(hello, data[5:5+size]...)
		data = data[5+size:]
		if need < 0 && len(hello) >= 4 {
			if hello[0] != 1 {
				return "", false, errNotClientHello
			}
			need = 4 + (int(hello[1])<<16 | int(hello[2])<<8 | int(hello[3]))
			if need > MaxClientHello {
				return "", false, errNotClientHello
			}
		}
		if need >= 0 && len(hello) >= need {
			name, err := helloServerName(hello[4:need])
			return name, err == nil, err
		}
	}
	return "", false, nil
}

// helloServerName finds the server_name extension in the body of a
// ClientHello.
func helloServerName(b []byte) (string, error) {
	// The version and random, then the session id, cipher suites and
	// compression methods, each after its length.
	if len(b) < 2+32+1 {
		return "", errNotClientHello
	}
	b = b[2+32:]
	var ok bool
	for _, lenBytes := range [...]int{1, 2, 1} {
		if b, ok = skipVector(b, lenBytes); !ok {
			return "", errNotClientHello
		}
	}
	if len(b) == 0 {
		return "", nil // no extensions
	}
	if len(b) < 2 || int(binary.BigEndian.Uint16(b)) > len(b)-2 {
		return "", errNotClientHello
	}
	exts := b[2 : 2+int(binary.BigEndian.Uint16(b))]
	for len(exts) >= 4 {
		typ, size := binary.BigEndian.Uint16(exts), int(binary.BigEndian.Uint16(exts[2:]))
		if size > len(exts)-4 {
			return "", errNotClientHello
		}
		body := exts[4 : 4+size]
		exts = exts[4+size:]
		if typ != 0 { // server_name
			continue
		}
		// A list of names, of which host_name (0) is the only kind.
		if len(body) < 2 || int(binary.BigEndian.Uint16(body)) != len(body)-2 {
			return "", errNotClientHello
		}
		for list := body[2:]; len(list) >= 3; {
			kind, n := list[0], int(binary.BigEndian.Uint16(list[1:]))
			if n > len(list)-3 {
				return "", errNotClientHello
			}
			if kind == 0 {
				return strings.ToLower(strings.TrimSuffix(string(list[3:3+n]), ".")), nil
			}
			list = list[3+n:]
		}
		return "", nil
	}
	return "", nil
}

// skipVector skips a vector whose length takes lenBytes bytes.
func skipVector(b []byte, lenBytes int) ([]byte, bool) {
	if len(b) < lenBytes {
		return nil, false
	}
	n := int(b[0])
	if lenBytes == 2 {
		n = int(binary.BigEndian.Uint16(b))
	}
	if len(b) < lenBytes+n {
		return nil, false
	}
	return b[lenBytes+n:], true
}
