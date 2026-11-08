package mongowire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
)

// OP_MSG: a 16-byte header (length, request ID, response to, op code), a
// uint32 of flags, then sections: kind 0 is the command's body, one
// document; kind 1 is a sequence of documents with an identifier, as an
// insert's "documents". A CRC-32C of the message ends it when the
// checksum flag is set.

const (
	opMsg      = 2013
	headerSize = 16

	flagChecksum   = 1 << 0
	flagMoreToCome = 1 << 1
	// The flags a reader must know are bits 0 to 15.
	flagsRequired = 0xFFFF
)

var castagnoli = crc32.MakeTable(crc32.Castagnoli)

// Sequence is a kind 1 section: documents sent beside the body, under the
// name of the body's field they stand for, as "documents" for an insert,
// "updates" for an update and "deletes" for a delete.
type Sequence struct {
	ID   string
	Docs []Raw
}

// appendMsg appends an OP_MSG of body and seqs with request ID id.
func appendMsg(dst []byte, id int32, body Raw, seqs []Sequence) []byte {
	start := len(dst)
	dst = binary.LittleEndian.AppendUint32(dst, 0)
	dst = binary.LittleEndian.AppendUint32(dst, uint32(id))
	dst = binary.LittleEndian.AppendUint32(dst, 0)
	dst = binary.LittleEndian.AppendUint32(dst, opMsg)
	dst = binary.LittleEndian.AppendUint32(dst, 0)
	dst = append(dst, 0)
	dst = append(dst, body...)
	for _, s := range seqs {
		dst = append(dst, 1)
		at := len(dst)
		dst = binary.LittleEndian.AppendUint32(dst, 0)
		dst = append(append(dst, s.ID...), 0)
		for _, d := range s.Docs {
			dst = append(dst, d...)
		}
		binary.LittleEndian.PutUint32(dst[at:], uint32(len(dst)-at))
	}
	binary.LittleEndian.PutUint32(dst[start:], uint32(len(dst)-start))
	return dst
}

// msgSize is the size of the message appendMsg would write.
func msgSize(body Raw, seqs []Sequence) int {
	n := headerSize + 4 + 1 + len(body)
	for _, s := range seqs {
		n += 1 + 4 + len(s.ID) + 1
		for _, d := range s.Docs {
			n += len(d)
		}
	}
	return n
}

// parseMsg reads an OP_MSG whose header has been read: msg is the whole
// message, its header included. The documents it returns share msg.
func parseMsg(msg []byte) (flags uint32, body Raw, seqs []Sequence, err error) {
	if len(msg) < headerSize+4+1 {
		return 0, nil, nil, malformed("an OP_MSG of %d bytes", len(msg))
	}
	flags = binary.LittleEndian.Uint32(msg[headerSize:])
	if flags&flagsRequired&^(flagChecksum|flagMoreToCome) != 0 {
		return 0, nil, nil, fmt.Errorf("mongowire: an OP_MSG with unknown required flags %#x", flags)
	}
	b := msg[headerSize+4:]
	if flags&flagChecksum != 0 {
		if len(b) < 4 {
			return 0, nil, nil, malformed("an OP_MSG's checksum cut")
		}
		end := len(msg) - 4
		if crc32.Checksum(msg[:end], castagnoli) != binary.LittleEndian.Uint32(msg[end:]) {
			return 0, nil, nil, errors.New("mongowire: an OP_MSG's checksum does not match")
		}
		b = b[:len(b)-4]
	}
	for len(b) > 0 {
		kind := b[0]
		b = b[1:]
		switch kind {
		case 0:
			if body != nil {
				return 0, nil, nil, malformed("an OP_MSG with two bodies")
			}
			n, err := docSize(b, 0)
			if err != nil {
				return 0, nil, nil, err
			}
			body, b = Raw(b[:n]), b[n:]
		case 1:
			if len(b) < 4 {
				return 0, nil, nil, malformed("a sequence's length cut")
			}
			n := int(int32(binary.LittleEndian.Uint32(b)))
			if n < 5 || n > len(b) {
				return 0, nil, nil, malformed("a sequence's length %d", n)
			}
			sec := b[4:n]
			b = b[n:]
			i := cstringEnd(sec)
			if i < 0 {
				return 0, nil, nil, malformed("a sequence's name not ended by 0")
			}
			s := Sequence{ID: string(sec[:i])}
			sec = sec[i+1:]
			for len(sec) > 0 {
				dn, err := docSize(sec, 0)
				if err != nil {
					return 0, nil, nil, err
				}
				s.Docs = append(s.Docs, Raw(sec[:dn]))
				sec = sec[dn:]
			}
			seqs = append(seqs, s)
		default:
			return 0, nil, nil, malformed("an OP_MSG section of kind %d", kind)
		}
	}
	if body == nil {
		return 0, nil, nil, malformed("an OP_MSG without a body")
	}
	return flags, body, seqs, nil
}
