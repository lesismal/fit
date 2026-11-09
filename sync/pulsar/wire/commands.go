package pulsarwire

import (
	"fmt"
	"math"
	"strconv"
)

// The types of BaseCommand, which are also the numbers of the fields that
// hold each command.
const (
	typeConnect                     = 2
	typeConnected                   = 3
	typeSubscribe                   = 4
	typeProducer                    = 5
	typeSend                        = 6
	typeSendReceipt                 = 7
	typeSendError                   = 8
	typeMessage                     = 9
	typeAck                         = 10
	typeFlow                        = 11
	typeSuccess                     = 13
	typeError                       = 14
	typeCloseProducer               = 15
	typeCloseConsumer               = 16
	typeProducerSuccess             = 17
	typePing                        = 18
	typePong                        = 19
	typePartitionedMetadata         = 21
	typePartitionedMetadataResponse = 22
	typeLookup                      = 23
	typeLookupResponse              = 24
	typeReachedEndOfTopic           = 27
	typeGetLastMessageID            = 29
	typeGetLastMessageIDResponse    = 30
	typeAuthChallenge               = 36
	typeAuthResponse                = 37
)

// protocolVersion is the version of the protocol the client speaks: v19,
// Pulsar 2.10's, which the brokers of 2.10 and later speak too.
const protocolVersion = 19

// The lookup responses' types.
const (
	lookupRedirect = 0
	lookupConnect  = 1
	lookupFailed   = 2
)

// SubType is a subscription's type.
type SubType int32

// The subscription types the client takes.
const (
	Exclusive SubType = 0
	Failover  SubType = 2
)

// InitialPosition is where a new subscription starts.
type InitialPosition int32

// The initial positions.
const (
	Latest   InitialPosition = 0
	Earliest InitialPosition = 1
)

// MessageID is a message's place in a topic's ledgers: its ledger and entry,
// the partition, and its index in the entry's batch (-1 when the entry is a
// single message).
type MessageID struct {
	Ledger, Entry int64
	Partition     int32
	BatchIndex    int32
	// BatchSize is how many messages the entry's batch holds; 0 when it is
	// not a batch.
	BatchSize int32
}

// EarliestID and LatestID are the IDs a reader starts at for a topic's
// first message, and after its last.
var (
	EarliestID = MessageID{Ledger: -1, Entry: -1, Partition: -1, BatchIndex: -1}
	LatestID   = MessageID{Ledger: math.MaxInt64, Entry: math.MaxInt64, Partition: -1, BatchIndex: -1}
)

func (m MessageID) String() string {
	return strconv.FormatInt(m.Ledger, 10) + ":" + strconv.FormatInt(m.Entry, 10) + ":" +
		strconv.Itoa(int(m.Partition)) + ":" + strconv.Itoa(int(m.BatchIndex))
}

// Compare orders m and o by ledger, entry and batch index, an index of -1
// (an entry that is not a batch, or the whole of one) after every other.
func (m MessageID) Compare(o MessageID) int {
	switch {
	case m.Ledger != o.Ledger:
		return cmp64(m.Ledger, o.Ledger)
	case m.Entry != o.Entry:
		return cmp64(m.Entry, o.Entry)
	}
	return cmp64(index(m.BatchIndex), index(o.BatchIndex))
}

func index(i int32) int64 {
	if i < 0 {
		return math.MaxInt64
	}
	return int64(i)
}

func cmp64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func (b pb) messageID(f int, m MessageID) pb {
	in := pb(nil).uint(1, uint64(m.Ledger)).uint(2, uint64(m.Entry)).int(3, int64(m.Partition)).int(4, int64(m.BatchIndex))
	if m.BatchSize > 0 {
		in = in.int(6, int64(m.BatchSize))
	}
	return b.bytes(f, in)
}

func decodeMessageID(b []byte) (MessageID, error) {
	m := MessageID{Partition: -1, BatchIndex: -1}
	r := pbReader{b: b}
	for f, ok := r.next(); ok; f, ok = r.next() {
		switch f {
		case 1:
			m.Ledger = r.int()
		case 2:
			m.Entry = r.int()
		case 3:
			m.Partition = int32(r.int())
		case 4:
			m.BatchIndex = int32(r.int())
		case 6:
			m.BatchSize = int32(r.int())
		default:
			r.skip()
		}
	}
	return m, r.err
}

// KeyValue is a message's property.
type KeyValue struct{ Key, Value string }

func (b pb) properties(f int, kvs []KeyValue) pb {
	for _, kv := range kvs {
		b = b.bytes(f, pb(nil).str(1, kv.Key).str(2, kv.Value))
	}
	return b
}

func decodeKeyValue(b []byte) (KeyValue, error) {
	var kv KeyValue
	r := pbReader{b: b}
	for f, ok := r.next(); ok; f, ok = r.next() {
		switch f {
		case 1:
			kv.Key = r.str()
		case 2:
			kv.Value = r.str()
		default:
			r.skip()
		}
	}
	return kv, r.err
}

// base wraps a command of type t in a BaseCommand.
func base(t int, inner pb) []byte {
	return pb(nil).uint(1, uint64(t)).bytes(t, inner)
}

// command is a command the broker sent, the fields the client reads of
// every type in one.
type command struct {
	typ int

	requestID, producerID, consumerID, sequenceID uint64

	// CONNECTED, AUTH_CHALLENGE.
	serverVersion  string
	maxMessageSize int32
	authMethod     string
	// ERROR, SEND_ERROR and the failures of the responses.
	serverError int32
	message     string
	failed      bool
	// PARTITIONED_METADATA_RESPONSE.
	partitions uint32
	// LOOKUP_RESPONSE.
	brokerURL, brokerURLTLS string
	lookupType              int
	authoritative, proxy    bool
	// PRODUCER_SUCCESS.
	producerName   string
	lastSequenceID int64
	producerReady  bool
	// SEND_RECEIPT, MESSAGE, GET_LAST_MESSAGE_ID_RESPONSE.
	messageID  MessageID
	redelivery uint32
}

// decodeCommand reads a BaseCommand.
func decodeCommand(b []byte) (*command, error) {
	c := &command{lastSequenceID: -1, producerReady: true}
	var inner []byte
	r := pbReader{b: b}
	fields := map[int][]byte{}
	for f, ok := r.next(); ok; f, ok = r.next() {
		if f == 1 {
			c.typ = int(r.uint())
		} else if r.wt == wireBytes {
			fields[f] = r.bytes()
		} else {
			r.skip()
		}
	}
	if r.err != nil || c.typ == 0 {
		return nil, errProto
	}
	inner = fields[c.typ]
	r = pbReader{b: inner}
	var err error
	for f, ok := r.next(); ok && err == nil; f, ok = r.next() {
		err = c.field(&r, f)
	}
	if err == nil {
		err = r.err
	}
	if err != nil {
		return nil, fmt.Errorf("pulsar: command %d: %w", c.typ, err)
	}
	return c, nil
}

// field reads field f of c's command.
func (c *command) field(r *pbReader, f int) error {
	var err error
	switch c.typ<<8 | f {
	case typeConnected<<8 | 1, typeAuthChallenge<<8 | 1:
		c.serverVersion = r.str()
	case typeConnected<<8 | 3:
		c.maxMessageSize = int32(r.int())
	case typeAuthChallenge<<8 | 2:
		ar := pbReader{b: r.bytes()}
		for f, ok := ar.next(); ok; f, ok = ar.next() {
			if f == 1 {
				c.authMethod = ar.str()
			} else {
				ar.skip()
			}
		}
		err = ar.err
	case typePartitionedMetadataResponse<<8 | 1:
		c.partitions = uint32(r.uint())
	case typePartitionedMetadataResponse<<8 | 2, typeLookupResponse<<8 | 4, typeProducerSuccess<<8 | 1,
		typeSuccess<<8 | 1, typeError<<8 | 1, typeGetLastMessageIDResponse<<8 | 2:
		c.requestID = r.uint()
	case typePartitionedMetadataResponse<<8 | 3:
		c.failed = r.uint() == 1
	case typePartitionedMetadataResponse<<8 | 4, typeLookupResponse<<8 | 6, typeError<<8 | 2, typeSendError<<8 | 3:
		c.serverError = int32(r.int())
	case typePartitionedMetadataResponse<<8 | 5, typeLookupResponse<<8 | 7, typeError<<8 | 3, typeSendError<<8 | 4:
		c.message = r.str()
	case typeLookupResponse<<8 | 1:
		c.brokerURL = r.str()
	case typeLookupResponse<<8 | 2:
		c.brokerURLTLS = r.str()
	case typeLookupResponse<<8 | 3:
		c.lookupType = int(r.uint())
		c.failed = c.lookupType == lookupFailed
	case typeLookupResponse<<8 | 5:
		c.authoritative = r.uint() != 0
	case typeLookupResponse<<8 | 8:
		c.proxy = r.uint() != 0
	case typeProducerSuccess<<8 | 2:
		c.producerName = r.str()
	case typeProducerSuccess<<8 | 3:
		c.lastSequenceID = r.int()
	case typeProducerSuccess<<8 | 6:
		c.producerReady = r.uint() != 0
	case typeSendReceipt<<8 | 1, typeSendError<<8 | 1, typeCloseProducer<<8 | 1:
		c.producerID = r.uint()
	case typeSendReceipt<<8 | 2, typeSendError<<8 | 2:
		c.sequenceID = r.uint()
	case typeSendReceipt<<8 | 3, typeMessage<<8 | 2, typeGetLastMessageIDResponse<<8 | 1:
		c.messageID, err = decodeMessageID(r.bytes())
	case typeMessage<<8 | 1, typeCloseConsumer<<8 | 1, typeReachedEndOfTopic<<8 | 1:
		c.consumerID = r.uint()
	case typeMessage<<8 | 3:
		c.redelivery = uint32(r.uint())
	default:
		r.skip()
	}
	return err
}

// The commands the client sends.

func connectCmd(clientVersion, token string, proxyTo string) []byte {
	in := pb(nil).str(1, clientVersion)
	if token != "" {
		in = in.bytes(3, []byte(token)).str(5, "token")
	}
	in = in.int(4, protocolVersion)
	if proxyTo != "" {
		in = in.str(6, proxyTo)
	}
	// feature_flags: supports_auth_refresh.
	in = in.bytes(10, pb(nil).bool(1, true))
	return base(typeConnect, in)
}

func authResponseCmd(clientVersion, method, token string) []byte {
	return base(typeAuthResponse, pb(nil).str(1, clientVersion).
		bytes(2, pb(nil).str(1, method).bytes(2, []byte(token))).int(3, protocolVersion))
}

func partitionedMetadataCmd(topic string, req uint64) []byte {
	return base(typePartitionedMetadata, pb(nil).str(1, topic).uint(2, req))
}

func lookupCmd(topic string, req uint64, authoritative bool) []byte {
	return base(typeLookup, pb(nil).str(1, topic).uint(2, req).bool(3, authoritative))
}

func producerCmd(topic string, id, req uint64, name string) []byte {
	in := pb(nil).str(1, topic).uint(2, id).uint(3, req)
	if name != "" {
		in = in.str(4, name).bool(9, true)
	}
	return base(typeProducer, in)
}

func sendCmd(producer, seq uint64, n int, highest uint64) []byte {
	return base(typeSend, pb(nil).uint(1, producer).uint(2, seq).int(3, int64(n)).uint(6, highest))
}

// subscribeOpts are what a SUBSCRIBE says.
type subscribeCmdOpts struct {
	topic, subscription, consumerName string
	subType                           SubType
	consumer, req                     uint64
	durable                           bool
	start                             *MessageID
	initial                           InitialPosition
}

func subscribeCmd(o subscribeCmdOpts) []byte {
	in := pb(nil).str(1, o.topic).str(2, o.subscription).int(3, int64(o.subType)).uint(4, o.consumer).uint(5, o.req)
	if o.consumerName != "" {
		in = in.str(6, o.consumerName)
	}
	in = in.bool(8, o.durable)
	if o.start != nil {
		in = in.messageID(9, *o.start)
	}
	in = in.int(13, int64(o.initial))
	return base(typeSubscribe, in)
}

func flowCmd(consumer uint64, permits int) []byte {
	return base(typeFlow, pb(nil).uint(1, consumer).uint(2, uint64(permits)))
}

func ackCmd(consumer uint64, id MessageID) []byte {
	return base(typeAck, pb(nil).uint(1, consumer).uint(2, 1).messageID(3, id))
}

func closeProducerCmd(id, req uint64) []byte {
	return base(typeCloseProducer, pb(nil).uint(1, id).uint(2, req))
}

func closeConsumerCmd(id, req uint64) []byte {
	return base(typeCloseConsumer, pb(nil).uint(1, id).uint(2, req))
}

func getLastMessageIDCmd(consumer, req uint64) []byte {
	return base(typeGetLastMessageID, pb(nil).uint(1, consumer).uint(2, req))
}

var (
	pingCmd = base(typePing, nil)
	pongCmd = base(typePong, nil)
)
