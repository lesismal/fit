package binlog

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
)

// Magic begins every binlog file.
var Magic = []byte{0xfe, 'b', 'i', 'n'}

// Reader reads the events of a binlog file.
type Reader struct {
	// Decoder decodes the events; its bounds apply.
	*Decoder
	br  *bufio.Reader
	buf []byte
	pos int64
}

// NewReader returns a Reader of the binlog file r, after checking its
// magic number.
func NewReader(r io.Reader) (*Reader, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	var m [4]byte
	if _, err := io.ReadFull(br, m[:]); err != nil {
		return nil, fmt.Errorf("binlog: reading the magic number: %w", err)
	}
	if !bytes.Equal(m[:], Magic) {
		return nil, fmt.Errorf("%w: not a binlog file", ErrMalformed)
	}
	return &Reader{Decoder: NewDecoder(), br: br, pos: 4}, nil
}

// Position is the offset of the next event in the file.
func (r *Reader) Position() int64 { return r.pos }

// Next reads and decodes the next event; io.EOF at the file's end. An
// encrypted file (MariaDB's Start_encryption) is refused.
func (r *Reader) Next() (*Event, error) {
	h, err := r.br.Peek(HeaderSize)
	if err != nil {
		if err == io.EOF && len(h) == 0 {
			return nil, io.EOF
		}
		return nil, fmt.Errorf("binlog: reading an event at %d: %w", r.pos, noEOF(err))
	}
	hd, _ := ParseHeader(h)
	n := int(hd.Size)
	if n < HeaderSize {
		return nil, malformed("an event of %d bytes at %d", n, r.pos)
	}
	if n > r.maxEvent() {
		return nil, fmt.Errorf("%w: an event of %d bytes at %d, past MaxEventSize %d", ErrTooLarge, n, r.pos, r.maxEvent())
	}
	if cap(r.buf) > 1<<20 && n <= 1<<20 {
		r.buf = nil
	}
	// The buffer grows with what is read, not with what the header says,
	// which a cut or corrupt file may make large.
	b := r.buf[:0]
	for len(b) < n {
		k := min(n-len(b), max(len(b), 64<<10))
		if need := len(b) + k; need > cap(b) {
			nb := make([]byte, len(b), need)
			copy(nb, b)
			b = nb
		}
		b = b[:len(b)+k]
		if _, err := io.ReadFull(r.br, b[len(b)-k:]); err != nil {
			return nil, fmt.Errorf("binlog: reading an event at %d: %w", r.pos, noEOF(err))
		}
	}
	r.buf = b
	ev, err := r.Decode(b)
	if err != nil {
		return nil, fmt.Errorf("at %d: %w", r.pos, err)
	}
	r.pos += int64(n)
	if ev.Type == MariaStartEncryptionEvent {
		return nil, fmt.Errorf("%w: an encrypted binlog file", ErrUnsupported)
	}
	return ev, nil
}

func noEOF(err error) error {
	if errors.Is(err, io.EOF) {
		return io.ErrUnexpectedEOF
	}
	return err
}
