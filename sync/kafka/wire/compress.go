package kafkawire

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/klauspost/compress/s2"
	"github.com/klauspost/compress/snappy/xerial"
	"github.com/klauspost/compress/zstd"
)

// Compression is a record batch's codec, as its attributes number it.
type Compression int8

const (
	None Compression = iota
	Gzip
	Snappy
	LZ4
	Zstd
)

var compressionNames = [...]string{"none", "gzip", "snappy", "lz4", "zstd"}

func (c Compression) String() string {
	if c >= 0 && int(c) < len(compressionNames) {
		return compressionNames[c]
	}
	return fmt.Sprintf("compression(%d)", int8(c))
}

// ParseCompression reads a codec's name: none (or empty), gzip, snappy,
// lz4 or zstd.
func ParseCompression(s string) (Compression, error) {
	if s == "" {
		return None, nil
	}
	for i, n := range compressionNames {
		if strings.EqualFold(s, n) {
			return Compression(i), nil
		}
	}
	return None, fmt.Errorf("kafka: unknown compression %q (none, gzip, snappy, lz4, zstd)", s)
}

// errTooLarge is a batch whose records, decompressed, pass the limit.
var errTooLarge = errors.New("decompressed records past the limit")

// compress returns src compressed with c at level (0 for the codec's
// default; gzip 1 to 9, zstd 1 to 22; snappy and lz4 have none). Snappy
// is in xerial's framing and LZ4 in its frame format, as Kafka's Java
// client writes them.
func compress(c Compression, level int, src []byte) ([]byte, error) {
	switch c {
	case Gzip:
		if level == 0 {
			level = gzip.DefaultCompression
		}
		var buf bytes.Buffer
		w, err := gzip.NewWriterLevel(&buf, level)
		if err != nil {
			return nil, err
		}
		w.Write(src)
		w.Close() // into a buffer: no error
		return buf.Bytes(), nil
	case Snappy:
		return xerial.Encode(nil, src), nil
	case LZ4:
		return lz4Compress(nil, src), nil
	case Zstd:
		return zstdEncoder(level).EncodeAll(src, nil), nil
	}
	return nil, fmt.Errorf("kafka: cannot compress with %v", c)
}

var zstdEncoders sync.Map // level -> *zstd.Encoder

func zstdEncoder(level int) *zstd.Encoder {
	if e, ok := zstdEncoders.Load(level); ok {
		return e.(*zstd.Encoder)
	}
	l := zstd.SpeedDefault
	if level != 0 {
		l = zstd.EncoderLevelFromZstd(level)
	}
	// The options are valid, so there is no error.
	e, _ := zstd.NewWriter(nil, zstd.WithEncoderLevel(l), zstd.WithEncoderConcurrency(1))
	actual, _ := zstdEncoders.LoadOrStore(level, e)
	return actual.(*zstd.Encoder)
}

// decompressor decompresses record batches, each bounded by a limit, so
// that a small batch cannot expand into more memory than a fetch allows.
type decompressor struct {
	once sync.Once
	zstd *zstd.Decoder
	max  int // the zstd decoder's limit, the client's
}

func (d *decompressor) decompress(c Compression, src []byte, limit int) ([]byte, error) {
	switch c {
	case Gzip:
		r, err := gzip.NewReader(bytes.NewReader(src))
		if err != nil {
			return nil, err
		}
		out, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
		if err != nil {
			return nil, err
		}
		if len(out) > limit {
			return nil, errTooLarge
		}
		return out, nil
	case Snappy:
		return snappyDecode(src, limit)
	case LZ4:
		return lz4Decompress(nil, src, limit)
	case Zstd:
		d.once.Do(func() {
			// The options are valid, so there is no error.
			d.zstd, _ = zstd.NewReader(nil, zstd.WithDecoderConcurrency(0),
				zstd.WithDecoderLowmem(true), zstd.WithDecoderMaxMemory(uint64(max(d.max, 1))))
		})
		out, err := d.zstd.DecodeAll(src, nil)
		if err != nil {
			if errors.Is(err, zstd.ErrDecoderSizeExceeded) {
				return nil, errTooLarge
			}
			return nil, err
		}
		if len(out) > limit {
			return nil, errTooLarge
		}
		return out, nil
	}
	return nil, fmt.Errorf("kafka: unknown compression %v", c)
}

// xerialHeader begins snappy in xerial's framing (which the Java client
// writes); without it, the data is one raw snappy block (which other
// clients write).
var xerialHeader = []byte{0x82, 'S', 'N', 'A', 'P', 'P', 'Y', 0}

// snappyDecode reads raw or xerial-framed snappy, checking each block's
// declared length against the limit before it allocates.
func snappyDecode(src []byte, limit int) ([]byte, error) {
	if !bytes.HasPrefix(src, xerialHeader) {
		n, err := s2.DecodedLen(src)
		if err != nil {
			return nil, err
		}
		if n > limit {
			return nil, errTooLarge
		}
		return s2.Decode(nil, src)
	}
	if len(src) < 16 {
		return nil, xerial.ErrMalformed
	}
	src = src[16:] // header and two version ints
	var out []byte
	for len(src) > 0 {
		if len(src) < 4 {
			return nil, xerial.ErrMalformed
		}
		n := int(binary.BigEndian.Uint32(src))
		src = src[4:]
		if n > len(src) {
			return nil, xerial.ErrMalformed
		}
		dl, err := s2.DecodedLen(src[:n])
		if err != nil {
			return nil, err
		}
		if len(out)+dl > limit {
			return nil, errTooLarge
		}
		at := len(out)
		out = append(out, make([]byte, dl)...)
		if _, err := s2.Decode(out[at:], src[:n]); err != nil {
			return nil, err
		}
		src = src[n:]
	}
	return out, nil
}
