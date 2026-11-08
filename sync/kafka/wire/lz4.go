package kafkawire

import (
	"encoding/binary"
	"errors"
	"math/bits"
)

// LZ4 as Kafka uses it: the LZ4 frame format (magic 0x184D2204) around
// LZ4 blocks. Written here, as no LZ4 is among fit's dependencies (see the
// package doc). The encoder writes what Kafka's Java client writes:
// independent blocks of at most 64 KiB, no checksums but the header's.
// The decoder reads any frame: linked or independent blocks, block and
// content checksums (checked), a content size, blocks up to 4 MiB.

const (
	lz4Magic     = 0x184D2204
	lz4BlockSize = 64 << 10
	lz4MinMatch  = 4
	lz4LastLits  = 5  // a block ends with at least five literals
	lz4MFLimit   = 12 // a match starts at least twelve bytes before the end
	lz4HashLog   = 14
)

var errLZ4 = errors.New("lz4: corrupt frame")

// lz4Compress appends src as one LZ4 frame.
func lz4Compress(dst, src []byte) []byte {
	dst = binary.LittleEndian.AppendUint32(dst, lz4Magic)
	desc := []byte{0x60, 0x40} // version 1, independent blocks; 64 KiB blocks
	dst = append(dst, desc...)
	dst = append(dst, byte(xxh32(desc, 0)>>8))
	var table [1 << lz4HashLog]int32
	for len(src) > 0 {
		n := min(len(src), lz4BlockSize)
		at := len(dst)
		dst = append(dst, 0, 0, 0, 0)
		clear(table[:])
		dst = lz4CompressBlock(dst, src[:n], &table)
		if size := len(dst) - at - 4; size >= n {
			// Not smaller: stored as it is, the size's high bit set.
			dst = append(dst[:at+4], src[:n]...)
			binary.LittleEndian.PutUint32(dst[at:], uint32(n)|1<<31)
		} else {
			binary.LittleEndian.PutUint32(dst[at:], uint32(size))
		}
		src = src[n:]
	}
	return binary.LittleEndian.AppendUint32(dst, 0) // end mark
}

// lz4CompressBlock appends src as an LZ4 block, matching greedily on a
// hash of four bytes.
func lz4CompressBlock(dst, src []byte, table *[1 << lz4HashLog]int32) []byte {
	n := len(src)
	anchor, i := 0, 0
	if n >= lz4MFLimit+1 {
		limit, matchLimit := n-lz4MFLimit, n-lz4LastLits
		for i < limit {
			seq := binary.LittleEndian.Uint32(src[i:])
			h := (seq * 2654435761) >> (32 - lz4HashLog)
			ref := int(table[h]) - 1
			table[h] = int32(i + 1)
			if ref < 0 || i-ref > 0xFFFF || binary.LittleEndian.Uint32(src[ref:]) != seq {
				i++
				continue
			}
			m := i + lz4MinMatch
			for r := ref + lz4MinMatch; m < matchLimit && src[m] == src[r]; m, r = m+1, r+1 {
			}
			dst = lz4Sequence(dst, src[anchor:i], i-ref, m-i)
			i, anchor = m, m
		}
	}
	return lz4Sequence(dst, src[anchor:], 0, 0)
}

// lz4Sequence appends literals and a match (none when matchLen is 0, the
// block's last sequence).
func lz4Sequence(dst, lits []byte, offset, matchLen int) []byte {
	ll, ml := len(lits), 0
	if matchLen > 0 {
		ml = matchLen - lz4MinMatch
	}
	tok := byte(min(ll, 15))<<4 | byte(min(ml, 15))
	dst = append(dst, tok)
	dst = lz4Length(dst, ll)
	dst = append(dst, lits...)
	if matchLen == 0 {
		return dst
	}
	dst = append(dst, byte(offset), byte(offset>>8))
	return lz4Length(dst, ml)
}

// lz4Length appends the bytes of a length past the token's 15.
func lz4Length(dst []byte, n int) []byte {
	if n < 15 {
		return dst
	}
	n -= 15
	for ; n >= 255; n -= 255 {
		dst = append(dst, 255)
	}
	return append(dst, byte(n))
}

// lz4Decompress appends the content of the LZ4 frames in src to dst,
// refusing to go past limit bytes.
func lz4Decompress(dst, src []byte, limit int) ([]byte, error) {
	for len(src) > 0 {
		var err error
		if dst, src, err = lz4Frame(dst, src, limit); err != nil {
			return dst, err
		}
	}
	return dst, nil
}

func lz4Frame(dst, src []byte, limit int) ([]byte, []byte, error) {
	if len(src) < 7 || binary.LittleEndian.Uint32(src) != lz4Magic {
		return dst, nil, errLZ4
	}
	flg, bd := src[4], src[5]
	if flg>>6 != 1 {
		return dst, nil, errors.New("lz4: frame version not 1")
	}
	descLen := 2
	if flg&0x08 != 0 {
		descLen += 8 // content size, not needed
	}
	if flg&0x01 != 0 {
		return dst, nil, errors.New("lz4: frame with a dictionary")
	}
	if len(src) < 4+descLen+1 {
		return dst, nil, errLZ4
	}
	if byte(xxh32(src[4:4+descLen], 0)>>8) != src[4+descLen] {
		return dst, nil, errors.New("lz4: frame header checksum mismatch")
	}
	blockMax := 1 << (8 + 2*int((bd>>4)&7))
	if blockMax < 64<<10 {
		return dst, nil, errLZ4
	}
	blockSum, contentSum := flg&0x10 != 0, flg&0x04 != 0
	src = src[4+descLen+1:]
	start := len(dst)
	for {
		if len(src) < 4 {
			return dst, nil, errLZ4
		}
		size := binary.LittleEndian.Uint32(src)
		src = src[4:]
		if size == 0 {
			break
		}
		stored := size&(1<<31) != 0
		n := int(size &^ (1 << 31))
		if n > blockMax || n > len(src) {
			return dst, nil, errLZ4
		}
		block := src[:n]
		src = src[n:]
		if blockSum {
			if len(src) < 4 || xxh32(block, 0) != binary.LittleEndian.Uint32(src) {
				return dst, nil, errors.New("lz4: block checksum mismatch")
			}
			src = src[4:]
		}
		if stored {
			if len(dst)+n > limit {
				return dst, nil, errTooLarge
			}
			dst = append(dst, block...)
			continue
		}
		var err error
		// The whole output is the history: a linked block's matches
		// reach into the blocks before it.
		if dst, err = lz4DecompressBlock(dst, block, min(limit, len(dst)+blockMax)); err != nil {
			return dst, nil, err
		}
	}
	if contentSum {
		if len(src) < 4 || xxh32(dst[start:], 0) != binary.LittleEndian.Uint32(src) {
			return dst, nil, errors.New("lz4: content checksum mismatch")
		}
		src = src[4:]
	}
	return dst, src, nil
}

// lz4DecompressBlock appends the content of an LZ4 block to dst, whose
// bytes are the history matches may refer to, up to limit bytes in all.
func lz4DecompressBlock(dst, src []byte, limit int) ([]byte, error) {
	length := func(i, n int) (int, int, error) {
		if n != 15 {
			return i, n, nil
		}
		for {
			if i >= len(src) {
				return i, 0, errLZ4
			}
			b := src[i]
			i++
			n += int(b)
			if n > limit {
				return i, 0, errTooLarge
			}
			if b != 255 {
				return i, n, nil
			}
		}
	}
	i := 0
	for i < len(src) {
		tok := src[i]
		i++
		var ll int
		var err error
		if i, ll, err = length(i, int(tok>>4)); err != nil {
			return dst, err
		}
		if ll > len(src)-i {
			return dst, errLZ4
		}
		if len(dst)+ll > limit {
			return dst, errTooLarge
		}
		dst = append(dst, src[i:i+ll]...)
		i += ll
		if i == len(src) {
			return dst, nil // the last sequence has no match
		}
		if len(src)-i < 2 {
			return dst, errLZ4
		}
		off := int(src[i]) | int(src[i+1])<<8
		i += 2
		if off == 0 || off > len(dst) {
			return dst, errLZ4
		}
		var ml int
		if i, ml, err = length(i, int(tok&15)); err != nil {
			return dst, err
		}
		ml += lz4MinMatch
		if len(dst)+ml > limit {
			return dst, errTooLarge
		}
		from := len(dst) - off
		if off >= ml {
			dst = append(dst, dst[from:from+ml]...)
			continue
		}
		for k := range ml { // overlapping: a run
			dst = append(dst, dst[from+k])
		}
	}
	return dst, errLZ4 // a block ends with literals
}

// xxh32 is xxHash32, which LZ4 frames use for their checksums.
func xxh32(b []byte, seed uint32) uint32 {
	const (
		p1 uint32 = 2654435761
		p2 uint32 = 2246822519
		p3 uint32 = 3266489917
		p4 uint32 = 668265263
		p5 uint32 = 374761393
	)
	round := func(acc, in uint32) uint32 { return bits.RotateLeft32(acc+in*p2, 13) * p1 }
	n := len(b)
	var h uint32
	if n >= 16 {
		v1, v2, v3, v4 := seed+p1+p2, seed+p2, seed, seed-p1
		for len(b) >= 16 {
			v1 = round(v1, binary.LittleEndian.Uint32(b))
			v2 = round(v2, binary.LittleEndian.Uint32(b[4:]))
			v3 = round(v3, binary.LittleEndian.Uint32(b[8:]))
			v4 = round(v4, binary.LittleEndian.Uint32(b[12:]))
			b = b[16:]
		}
		h = bits.RotateLeft32(v1, 1) + bits.RotateLeft32(v2, 7) + bits.RotateLeft32(v3, 12) + bits.RotateLeft32(v4, 18)
	} else {
		h = seed + p5
	}
	h += uint32(n)
	for ; len(b) >= 4; b = b[4:] {
		h += binary.LittleEndian.Uint32(b) * p3
		h = bits.RotateLeft32(h, 17) * p4
	}
	for _, c := range b {
		h += uint32(c) * p5
		h = bits.RotateLeft32(h, 11) * p1
	}
	h ^= h >> 15
	h *= p2
	h ^= h >> 13
	h *= p3
	h ^= h >> 16
	return h
}
