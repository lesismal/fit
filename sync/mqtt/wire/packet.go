package mqttwire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"unicode/utf8"
)

// The control packets' types, the high four bits of the first byte.
const (
	typeConnect    byte = 1
	typeConnack    byte = 2
	typePublish    byte = 3
	typePuback     byte = 4
	typePubrec     byte = 5
	typePubrel     byte = 6
	typePubcomp    byte = 7
	typeSubscribe  byte = 8
	typeSuback     byte = 9
	typeUnsub      byte = 10
	typeUnsuback   byte = 11
	typePingreq    byte = 12
	typePingresp   byte = 13
	typeDisconnect byte = 14
	typeAuth       byte = 15
)

var typeNames = [...]string{"", "CONNECT", "CONNACK", "PUBLISH", "PUBACK", "PUBREC", "PUBREL", "PUBCOMP",
	"SUBSCRIBE", "SUBACK", "UNSUBSCRIBE", "UNSUBACK", "PINGREQ", "PINGRESP", "DISCONNECT", "AUTH"}

// The properties of MQTT 5 this package reads or writes, by identifier.
const (
	propPayloadFormat      = 0x01
	propMessageExpiry      = 0x02
	propContentType        = 0x03
	propResponseTopic      = 0x08
	propCorrelationData    = 0x09
	propSubscriptionID     = 0x0B
	propSessionExpiry      = 0x11
	propAssignedClientID   = 0x12
	propServerKeepAlive    = 0x13
	propAuthMethod         = 0x15
	propAuthData           = 0x16
	propRequestProblemInfo = 0x17
	propWillDelay          = 0x18
	propRequestRespInfo    = 0x19
	propResponseInfo       = 0x1A
	propServerReference    = 0x1C
	propReasonString       = 0x1F
	propReceiveMaximum     = 0x21
	propTopicAliasMaximum  = 0x22
	propTopicAlias         = 0x23
	propMaximumQoS         = 0x24
	propRetainAvailable    = 0x25
	propUserProperty       = 0x26
	propMaximumPacketSize  = 0x27
	propWildcardAvailable  = 0x28
	propSubIDAvailable     = 0x29
	propSharedAvailable    = 0x2A
)

// maxRemaining is the most a packet's remaining length can say: four bytes
// of variable byte integer.
const maxRemaining = 268435455

// MaxSubscriptionID is the largest subscription identifier: a variable
// byte integer's most.
const MaxSubscriptionID = 268435455

// errMalformed says a packet is not what MQTT says it must be.
var errMalformed = errors.New("mqtt: a malformed packet")

// UserProperty is an MQTT 5 user property: a name and a value, which may
// come more than once.
type UserProperty struct {
	Key, Value string
}

// props are the MQTT 5 properties of a packet this package keeps.
type props struct {
	payloadUTF8     bool
	expiry          uint32
	hasExpiry       bool
	contentType     string
	responseTopic   string
	correlation     []byte
	subIDs          []int
	sessionExpiry   uint32
	assignedID      string
	serverKeepAlive uint16
	hasKeepAlive    bool
	reason          string
	receiveMax      uint16
	aliasMax        uint16
	alias           uint16
	maxQoS          byte
	hasMaxQoS       bool
	retainAvailable byte
	hasRetain       bool
	subIDsAvailable byte
	hasSubIDs       bool
	maxPacket       uint32
	user            []UserProperty
	authMethod      string
	authData        []byte
	willDelay       uint32
}

// appendVarint appends n as a variable byte integer.
func appendVarint(b []byte, n int) []byte {
	for {
		c := byte(n & 0x7F)
		n >>= 7
		if n > 0 {
			c |= 0x80
		}
		b = append(b, c)
		if n == 0 {
			return b
		}
	}
}

func appendUint16(b []byte, n uint16) []byte { return binary.BigEndian.AppendUint16(b, n) }

func appendUint32(b []byte, n uint32) []byte { return binary.BigEndian.AppendUint32(b, n) }

func appendString(b []byte, s string) []byte {
	return append(appendUint16(b, uint16(len(s))), s...)
}

func appendBinary(b []byte, d []byte) []byte {
	return append(appendUint16(b, uint16(len(d))), d...)
}

// packet makes a packet of a first byte and its body: the fixed header's
// remaining length goes between them.
func packet(first byte, body []byte) []byte {
	b := make([]byte, 0, len(body)+5)
	b = append(b, first)
	b = appendVarint(b, len(body))
	return append(b, body...)
}

// propsWriter gathers a packet's properties, written with their length
// before them.
type propsWriter struct{ b []byte }

func (w *propsWriter) byte(id, v byte)       { w.b = append(w.b, id, v) }
func (w *propsWriter) u16(id byte, v uint16) { w.b = appendUint16(append(w.b, id), v) }
func (w *propsWriter) u32(id byte, v uint32) { w.b = appendUint32(append(w.b, id), v) }
func (w *propsWriter) str(id byte, v string) { w.b = appendString(append(w.b, id), v) }
func (w *propsWriter) bin(id byte, v []byte) { w.b = appendBinary(append(w.b, id), v) }
func (w *propsWriter) user(ups []UserProperty) {
	for _, u := range ups {
		w.b = appendString(appendString(append(w.b, propUserProperty), u.Key), u.Value)
	}
}

// appendTo appends the properties' length and the properties.
func (w *propsWriter) appendTo(b []byte) []byte { return append(appendVarint(b, len(w.b)), w.b...) }

// reader reads a packet's body.
type reader struct {
	b   []byte
	off int
	err error
}

func (r *reader) fail() {
	if r.err == nil {
		r.err = errMalformed
	}
}

func (r *reader) left() int { return len(r.b) - r.off }

func (r *reader) take(n int) []byte {
	if r.err != nil || n > r.left() {
		r.fail()
		return nil
	}
	v := r.b[r.off : r.off+n]
	r.off += n
	return v
}

func (r *reader) byte() byte {
	if v := r.take(1); v != nil {
		return v[0]
	}
	return 0
}

func (r *reader) u16() uint16 {
	if v := r.take(2); v != nil {
		return binary.BigEndian.Uint16(v)
	}
	return 0
}

func (r *reader) u32() uint32 {
	if v := r.take(4); v != nil {
		return binary.BigEndian.Uint32(v)
	}
	return 0
}

func (r *reader) varint() int {
	n, shift := 0, 0
	for i := 0; i < 4; i++ {
		c := r.byte()
		if r.err != nil {
			return 0
		}
		n |= int(c&0x7F) << shift
		if c&0x80 == 0 {
			return n
		}
		shift += 7
	}
	r.fail()
	return 0
}

func (r *reader) binary() []byte {
	n := int(r.u16())
	if r.err != nil {
		return nil
	}
	v := r.take(n)
	if v == nil {
		return nil
	}
	return append([]byte{}, v...)
}

// string reads a UTF-8 string, which may not hold a null character.
func (r *reader) string() string {
	b := r.binary()
	if r.err == nil && !validString(string(b)) {
		r.fail()
	}
	return string(b)
}

// validString reports whether s is a string MQTT takes: UTF-8, without
// null characters, at most 65535 bytes.
func validString(s string) bool {
	if len(s) > 65535 || !utf8.ValidString(s) {
		return false
	}
	for _, c := range s {
		if c == 0 {
			return false
		}
	}
	return true
}

// props reads the properties of an MQTT 5 packet.
func (r *reader) props() props {
	var p props
	n := r.varint()
	if r.err != nil || n > r.left() {
		r.fail()
		return p
	}
	end := r.off + n
	for r.err == nil && r.off < end {
		id := r.byte()
		switch id {
		case propPayloadFormat:
			p.payloadUTF8 = r.byte() == 1
		case propMessageExpiry:
			p.expiry, p.hasExpiry = r.u32(), true
		case propContentType:
			p.contentType = r.string()
		case propResponseTopic:
			p.responseTopic = r.string()
		case propCorrelationData:
			p.correlation = r.binary()
		case propSubscriptionID:
			p.subIDs = append(p.subIDs, r.varint())
		case propSessionExpiry:
			p.sessionExpiry = r.u32()
		case propAssignedClientID:
			p.assignedID = r.string()
		case propServerKeepAlive:
			p.serverKeepAlive, p.hasKeepAlive = r.u16(), true
		case propReasonString:
			p.reason = r.string()
		case propReceiveMaximum:
			p.receiveMax = r.u16()
		case propTopicAliasMaximum:
			p.aliasMax = r.u16()
		case propTopicAlias:
			p.alias = r.u16()
		case propMaximumQoS:
			p.maxQoS, p.hasMaxQoS = r.byte(), true
		case propRetainAvailable:
			p.retainAvailable, p.hasRetain = r.byte(), true
		case propMaximumPacketSize:
			p.maxPacket = r.u32()
		case propUserProperty:
			k := r.string()
			p.user = append(p.user, UserProperty{k, r.string()})
		case propAuthMethod:
			p.authMethod = r.string()
		case propAuthData:
			p.authData = r.binary()
		case propResponseInfo, propServerReference:
			r.string()
		case propWillDelay:
			p.willDelay = r.u32()
		case propSubIDAvailable:
			p.subIDsAvailable, p.hasSubIDs = r.byte(), true
		case propRequestProblemInfo, propRequestRespInfo, propWildcardAvailable, propSharedAvailable:
			r.byte()
		default:
			r.fail()
		}
	}
	if r.off != end {
		r.fail()
	}
	return p
}

// ReasonError is a reason code of 0x80 or more an MQTT 5 server answers
// with, or a CONNACK's refusal in either version.
type ReasonError struct {
	// Packet is the packet that carried it: CONNACK, PUBACK, PUBREC,
	// SUBACK, UNSUBACK, AUTH, DISCONNECT.
	Packet string
	Code   byte
	// Reason is the server's reason string, when it sent one.
	Reason string
}

func (e *ReasonError) Error() string {
	s := fmt.Sprintf("mqtt: %s 0x%02X %s", e.Packet, e.Code, reasonName(e.Code))
	if e.Reason != "" {
		s += ": " + e.Reason
	}
	return s
}

// reasonNames name MQTT 5's reason codes of failure.
var reasonNames = map[byte]string{
	0x04: "disconnect with will message", 0x11: "no subscription existed",
	0x18: "continue authentication", 0x19: "re-authenticate",
	0x80: "unspecified error", 0x81: "malformed packet", 0x82: "protocol error", 0x83: "implementation specific error",
	0x84: "unsupported protocol version", 0x85: "client identifier not valid", 0x86: "bad user name or password",
	0x87: "not authorized", 0x88: "server unavailable", 0x89: "server busy", 0x8A: "banned",
	0x8B: "server shutting down", 0x8C: "bad authentication method", 0x8D: "keep alive timeout",
	0x8E: "session taken over", 0x8F: "topic filter invalid", 0x90: "topic name invalid",
	0x91: "packet identifier in use", 0x92: "packet identifier not found", 0x93: "receive maximum exceeded",
	0x94: "topic alias invalid", 0x95: "packet too large", 0x96: "message rate too high", 0x97: "quota exceeded",
	0x98: "administrative action", 0x99: "payload format invalid", 0x9A: "retain not supported",
	0x9B: "QoS not supported", 0x9C: "use another server", 0x9D: "server moved",
	0x9E: "shared subscriptions not supported", 0x9F: "connection rate exceeded", 0xA0: "maximum connect time",
	0xA1: "subscription identifiers not supported", 0xA2: "wildcard subscriptions not supported",
}

// connackNames311 name MQTT 3.1.1's CONNACK return codes.
var connackNames311 = map[byte]string{
	1: "unacceptable protocol version", 2: "identifier rejected", 3: "server unavailable",
	4: "bad user name or password", 5: "not authorized",
}

func reasonName(code byte) string {
	if n, ok := reasonNames[code]; ok {
		return n
	}
	return "unknown reason"
}

// ConnectError is a CONNACK's refusal.
type ConnectError struct {
	// Version is the protocol's, which says how Code reads.
	Version byte
	Code    byte
	Reason  string
}

func (e *ConnectError) Error() string {
	if e.Version == Version311 {
		n, ok := connackNames311[e.Code]
		if !ok {
			n = "unknown return code"
		}
		return fmt.Sprintf("mqtt: the server refused the connection: %d %s", e.Code, n)
	}
	s := fmt.Sprintf("mqtt: the server refused the connection: 0x%02X %s", e.Code, reasonName(e.Code))
	if e.Reason != "" {
		s += ": " + e.Reason
	}
	return s
}

// Auth reports whether the refusal is of the credentials: a bad user name
// or password, or not authorized.
func (e *ConnectError) Auth() bool {
	if e.Version == Version311 {
		return e.Code == 4 || e.Code == 5
	}
	return e.Code == 0x86 || e.Code == 0x87 || e.Code == 0x8C
}
