// Package wire is the mesh protocol: frames carried one per WebSocket message, and a Session that runs
// requests / responses, flow-controlled streams and pushes over any "send a frame / receive a frame" transport.
// It has no network code; the transport lives elsewhere.
//
// Package wire 是互联协议：一条 WebSocket 消息承载一个帧；Session 在任意“发送帧 / 接收帧”的传输上实现
// 请求 / 应答、带流控的流与推送。本包不含网络代码，传输在别处实现。
package wire

import (
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
)

// Type is the frame type.
// Type 帧类型。
type Type uint8

const (
	TypeRequest      Type = 1
	TypeResponse     Type = 2
	TypeStreamOpen   Type = 3
	TypeStreamData   Type = 4
	TypeStreamEnd    Type = 5
	TypeStreamCancel Type = 6
	TypePush         Type = 7
	TypePing         Type = 8
	TypePong         Type = 9
	TypeCredit       Type = 10
	// TypeStreamKeepalive resets a stream's idle timeout on both sides without carrying data (Stream.KeepAlive).
	// TypeStreamKeepalive 在不携带数据的情况下重置两端的流空闲超时（Stream.KeepAlive）。
	TypeStreamKeepalive Type = 11
	// TypeDrain tells the peer that this side is going away: no new calls, streams or pushes on this connection
	// (Session.Drain).
	// TypeDrain 告知对端本方即将下线：不要再在该连接上发起新的调用、流或推送（Session.Drain）。
	TypeDrain Type = 12
)

var typeNames = [...]string{"", "request", "response", "stream-open", "stream-data", "stream-end",
	"stream-cancel", "push", "ping", "pong", "credit", "stream-keepalive", "drain"}

func (t Type) String() string {
	if int(t) < len(typeNames) && t != 0 {
		return typeNames[t]
	}
	return fmt.Sprintf("type(%d)", t)
}

// hasHead reports whether frames of this type carry a method.
// hasHead 表示该类型的帧是否携带方法名。
func (t Type) hasHead() bool { return t == TypeRequest || t == TypeStreamOpen || t == TypePush }

// Flag is a bit set on the frame.
// Flag 帧标志位。
type Flag uint8

const (
	FlagGzip   Flag = 1 << 0 // payload is gzip-compressed / 载荷经 gzip 压缩
	FlagError  Flag = 1 << 1 // payload is an encoded *Error / 载荷是编码后的 *Error
	FlagBinary Flag = 1 << 2 // payload is binary, not JSON / 载荷为二进制而非 JSON
)

// Size limits.
// 大小限制。
const (
	headerSize      = 6   // type + flags + stream ID / 类型 + 标志 + 流 ID
	maxHeadField    = 255 // the method is length-prefixed by one byte / 方法名用一个字节表示长度
	DefaultMaxFrame = 16 << 20
	DefaultCompress = 1 << 10
)

var (
	ErrFrameTooLarge = errors.New("wire: frame too large")
	ErrBadFrame      = errors.New("wire: malformed frame")
)

// Head names the method of a request, stream or push. The session does not interpret it.
// Head 指明请求、流或推送的方法。会话不解释它。
type Head struct {
	Method string
}

// Frame is one protocol message.
// Frame 是一条协议消息。
type Frame struct {
	Type    Type
	Flags   Flag
	ID      uint32
	Head    Head // only for request / stream-open / push / 仅 request、stream-open、push 携带
	Payload []byte
}

// Marshal encodes f. Payloads of at least compressMin bytes are gzip-compressed when that makes them smaller
// (compressMin <= 0 disables compression). The encoded frame must not exceed maxFrame.
//
// Marshal 编码 f。载荷不少于 compressMin 字节且压缩后更小时使用 gzip（compressMin <= 0 关闭压缩）。
// 编码结果不得超过 maxFrame。
func (f *Frame) Marshal(compressMin, maxFrame int) ([]byte, error) {
	flags := f.Flags &^ FlagGzip
	payload := f.Payload
	if compressMin > 0 && len(payload) >= compressMin {
		if z := gzipBytes(payload); len(z) < len(payload) {
			payload, flags = z, flags|FlagGzip
		}
	}
	size := headerSize + len(payload)
	if f.Type.hasHead() {
		if len(f.Head.Method) > maxHeadField {
			return nil, fmt.Errorf("%w: method longer than %d bytes", ErrBadFrame, maxHeadField)
		}
		size += 1 + len(f.Head.Method)
	}
	if maxFrame > 0 && size > maxFrame {
		return nil, ErrFrameTooLarge
	}
	b := make([]byte, headerSize, size)
	b[0], b[1] = byte(f.Type), byte(flags)
	binary.BigEndian.PutUint32(b[2:], f.ID)
	if f.Type.hasHead() {
		b = appendField(b, f.Head.Method)
	}
	return append(b, payload...), nil
}

// Unmarshal decodes a frame; a compressed payload is inflated and must not exceed maxFrame (<= 0: no limit).
// Unmarshal 解码帧；压缩的载荷会被解压，解压后不得超过 maxFrame（<= 0 表示不限制）。
func Unmarshal(b []byte, maxFrame int) (*Frame, error) {
	if maxFrame > 0 && len(b) > maxFrame {
		return nil, ErrFrameTooLarge
	}
	if len(b) < headerSize {
		return nil, ErrBadFrame
	}
	f := &Frame{Type: Type(b[0]), Flags: Flag(b[1]), ID: binary.BigEndian.Uint32(b[2:])}
	if f.Type == 0 || f.Type > TypeDrain {
		return nil, fmt.Errorf("%w: unknown type %d", ErrBadFrame, b[0])
	}
	b = b[headerSize:]
	var ok bool
	if f.Type.hasHead() {
		if f.Head.Method, b, ok = readField(b); !ok {
			return nil, ErrBadFrame
		}
	}
	if f.Flags&FlagGzip != 0 {
		p, err := gunzipBytes(b, maxFrame)
		if err != nil {
			return nil, err
		}
		b = p
	}
	f.Payload = b
	f.Flags &^= FlagGzip
	return f, nil
}

func appendField(b []byte, s string) []byte { return append(append(b, byte(len(s))), s...) }

func readField(b []byte) (string, []byte, bool) {
	if len(b) < 1 || len(b) < 1+int(b[0]) {
		return "", nil, false
	}
	n := int(b[0])
	return string(b[1 : 1+n]), b[1+n:], true
}

var gzipWriters = sync.Pool{New: func() any { return gzip.NewWriter(nil) }}

func gzipBytes(p []byte) []byte {
	var buf bytes.Buffer
	w := gzipWriters.Get().(*gzip.Writer)
	w.Reset(&buf)
	_, _ = w.Write(p)
	_ = w.Close()
	gzipWriters.Put(w)
	return buf.Bytes()
}

func gunzipBytes(p []byte, limit int) ([]byte, error) {
	r, err := gzip.NewReader(bytes.NewReader(p))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadFrame, err)
	}
	defer r.Close()
	var src io.Reader = r
	if limit > 0 {
		src = io.LimitReader(r, int64(limit)+1)
	}
	out, err := io.ReadAll(src)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrBadFrame, err)
	}
	if limit > 0 && len(out) > limit {
		return nil, ErrFrameTooLarge
	}
	return out, nil
}
