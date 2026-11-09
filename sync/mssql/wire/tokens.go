package mssqlwire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strconv"
)

// The tokens of the server's answers.
const (
	tokColMetadata   = 0x81
	tokRow           = 0xd1
	tokNBCRow        = 0xd2
	tokDone          = 0xfd
	tokDoneProc      = 0xfe
	tokDoneInProc    = 0xff
	tokReturnStatus  = 0x79
	tokReturnValue   = 0xac
	tokError         = 0xaa
	tokInfo          = 0xab
	tokEnvChange     = 0xe3
	tokLoginAck      = 0xad
	tokOrder         = 0xa9
	tokFeatureExtAck = 0xae
	tokTabName       = 0xa4
	tokColInfo       = 0xa5
	tokSessionState  = 0xe4
	tokSSPI          = 0xed
)

// The DONE tokens' status bits.
const (
	doneMore     = 0x01
	doneError    = 0x02
	doneCount    = 0x10
	doneAttn     = 0x20
	doneSrvError = 0x100
)

// response reads one request's answer, token after token.
type response struct {
	c    *Conn
	errs []*Error
	// cols are the current result set's, vals the row read last.
	cols []Column
	vals []any
	// affected counts the rows the statements changed.
	affected int64
	// status is the procedure's return status, when hasStatus.
	status    int32
	hasStatus bool
	outputs   map[string]any
	// final says the answer's last DONE was read; attnAcked that it
	// acknowledged an attention.
	final     bool
	attnAcked bool
	begun     bool

	onLoginAck func()
}

// step reads the next token and does what it says, returning it.
func (r *response) step() (byte, error) {
	m := &r.c.msg
	if !r.begun {
		m.begin()
		r.begun = true
	}
	tok, err := m.u8()
	if err != nil {
		return 0, err
	}
	switch tok {
	case tokColMetadata:
		return tok, r.colMetadata()
	case tokRow:
		return tok, r.row(false)
	case tokNBCRow:
		return tok, r.row(true)
	case tokDone, tokDoneProc, tokDoneInProc:
		b, err := m.next(12)
		if err != nil {
			return 0, err
		}
		status := binary.LittleEndian.Uint16(b)
		if status&doneCount != 0 && tok != tokDoneProc {
			r.affected += int64(binary.LittleEndian.Uint64(b[4:]))
		}
		if status&doneAttn != 0 {
			r.attnAcked = true
		}
		if status&doneSrvError != 0 {
			r.c.fail(errors.New("mssql: the server reported a severe error and ended the session"))
		}
		if tok != tokDoneInProc && status&doneMore == 0 {
			r.final = true
		}
		return tok, nil
	case tokReturnStatus:
		v, err := m.u32()
		r.status, r.hasStatus = int32(v), true
		return tok, err
	case tokReturnValue:
		return tok, r.returnValue()
	case tokError, tokInfo:
		e, err := r.message()
		if err != nil {
			return 0, err
		}
		if tok == tokInfo {
			if r.c.cfg.OnInfo != nil {
				r.c.cfg.OnInfo(e)
			}
			return tok, nil
		}
		r.errs = append(r.errs, e)
		if e.Class >= 20 {
			r.c.fail(e)
		}
		return tok, nil
	case tokEnvChange:
		return tok, r.envChange()
	case tokLoginAck:
		n, err := m.u16()
		if err == nil {
			err = m.skip(int(n))
		}
		if err == nil && r.onLoginAck != nil {
			r.onLoginAck()
		}
		return tok, err
	case tokOrder, tokTabName, tokColInfo:
		n, err := m.u16()
		if err == nil {
			err = m.skip(int(n))
		}
		return tok, err
	case tokSessionState:
		n, err := m.u32()
		if err == nil {
			err = m.skip(int(n))
		}
		return tok, err
	case tokFeatureExtAck:
		for {
			id, err := m.u8()
			if err != nil || id == 0xff {
				return tok, err
			}
			n, err := m.u32()
			if err == nil {
				err = m.skip(int(n))
			}
			if err != nil {
				return 0, err
			}
		}
	}
	return 0, r.c.fail(fmt.Errorf("mssql: the server sent a token 0x%02x the client does not know", tok))
}

// err is the answer's error: its first server error, the others in More.
func (r *response) err() error {
	if len(r.errs) == 0 {
		return nil
	}
	e := *r.errs[0]
	e.More = r.errs[1:]
	return &e
}

// drain reads the rest of the answer, rows and all, and returns its error.
func (r *response) drain() error {
	for !r.final {
		if _, err := r.step(); err != nil {
			return err
		}
	}
	return r.err()
}

func (r *response) colMetadata() error {
	m := &r.c.msg
	n, err := m.u16()
	if err != nil {
		return err
	}
	if n == 0xffff {
		r.cols = nil
		return nil
	}
	cols := make([]Column, n)
	for i := range cols {
		c := &cols[i]
		b, err := m.next(6)
		if err != nil {
			return err
		}
		c.UserType, c.Flags = binary.LittleEndian.Uint32(b), binary.LittleEndian.Uint16(b[4:])
		if err := m.readTypeInfo(c, true); err != nil {
			return r.c.fail(err)
		}
		if c.Name, err = m.bVarchar(); err != nil {
			return err
		}
	}
	r.cols = cols
	r.vals = make([]any, n)
	return nil
}

func (r *response) row(nbc bool) error {
	m := &r.c.msg
	var bitmap []byte
	if nbc {
		b, err := m.next((len(r.cols) + 7) / 8)
		if err != nil {
			return err
		}
		bitmap = append(bitmap, b...)
	}
	for i := range r.cols {
		if nbc && bitmap[i/8]&(1<<(i%8)) != 0 {
			r.vals[i] = nil
			continue
		}
		v, err := m.readValue(&r.cols[i], r.c.cfg.MaxValueBytes)
		if err != nil {
			return r.c.fail(err)
		}
		r.vals[i] = v
	}
	return nil
}

func (r *response) returnValue() error {
	m := &r.c.msg
	if _, err := m.u16(); err != nil { // ordinal
		return err
	}
	name, err := m.bVarchar()
	if err != nil {
		return err
	}
	b, err := m.next(7) // status, user type, flags
	if err != nil {
		return err
	}
	c := Column{Name: name, UserType: binary.LittleEndian.Uint32(b[1:]), Flags: binary.LittleEndian.Uint16(b[5:])}
	if err := m.readTypeInfo(&c, false); err != nil {
		return r.c.fail(err)
	}
	v, err := m.readValue(&c, r.c.cfg.MaxValueBytes)
	if err != nil {
		return r.c.fail(err)
	}
	if r.outputs == nil {
		r.outputs = map[string]any{}
	}
	r.outputs[name] = v
	return nil
}

func (r *response) message() (*Error, error) {
	m := &r.c.msg
	n, err := m.u16()
	if err != nil {
		return nil, err
	}
	b, err := m.next(int(n))
	if err != nil {
		return nil, err
	}
	b = append([]byte(nil), b...)
	e := &Error{}
	bad := errors.New("mssql: a malformed error message")
	if len(b) < 8 {
		return nil, r.c.fail(bad)
	}
	e.Number, e.State, e.Class = int32(binary.LittleEndian.Uint32(b)), b[4], b[5]
	b = b[6:]
	str := func(wide int) (string, bool) {
		if len(b) < wide {
			return "", false
		}
		k := int(b[0])
		if wide == 2 {
			k = int(binary.LittleEndian.Uint16(b))
		}
		if len(b) < wide+2*k {
			return "", false
		}
		s := ucs2String(b[wide : wide+2*k])
		b = b[wide+2*k:]
		return s, true
	}
	var ok1, ok2, ok3 bool
	e.Message, ok1 = str(2)
	e.Server, ok2 = str(1)
	e.Proc, ok3 = str(1)
	if !ok1 || !ok2 || !ok3 || len(b) < 4 {
		return nil, r.c.fail(bad)
	}
	e.Line = int32(binary.LittleEndian.Uint32(b))
	return e, nil
}

// The ENVCHANGE types the client follows.
const (
	envDatabase   = 1
	envPacketSize = 4
	envCollation  = 7
	envBeginTx    = 8
	envCommitTx   = 9
	envRollbackTx = 10
	envTxEnded    = 17
)

func (r *response) envChange() error {
	m := &r.c.msg
	n, err := m.u16()
	if err != nil {
		return err
	}
	b, err := m.next(int(n))
	if err != nil {
		return err
	}
	if len(b) < 2 {
		return r.c.fail(errors.New("mssql: a malformed ENVCHANGE"))
	}
	c := r.c
	typ, b := b[0], b[1:]
	// The new value: a B_VARCHAR or a B_VARBYTE, by type.
	switch typ {
	case envDatabase, envPacketSize:
		k := int(b[0])
		if len(b) < 1+2*k {
			return c.fail(errors.New("mssql: a malformed ENVCHANGE"))
		}
		v := ucs2String(b[1 : 1+2*k])
		if typ == envDatabase {
			c.database = v
		} else if size, err := strconv.Atoi(v); err == nil && size >= minPacketSize && size <= maxPacketSize {
			c.packetSize = size
		}
	case envCollation, envBeginTx:
		k := int(b[0])
		if len(b) < 1+k {
			return c.fail(errors.New("mssql: a malformed ENVCHANGE"))
		}
		if typ == envCollation && k == 5 {
			copy(c.collation[:], b[1:6])
		}
		if typ == envBeginTx && k == 8 {
			copy(c.tx[:], b[1:9])
		}
	case envCommitTx, envRollbackTx, envTxEnded:
		c.tx = [8]byte{}
	}
	return nil
}
