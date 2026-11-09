package pulsarwire

import (
	"errors"
	"strconv"
)

// ServerError is an error the broker answered with: a ServerError code of
// Pulsar's and its message.
type ServerError struct {
	Code    int32
	Message string
}

var serverErrorNames = [...]string{"UnknownError", "MetadataError", "PersistenceError", "AuthenticationError",
	"AuthorizationError", "ConsumerBusy", "ServiceNotReady", "ProducerBlockedQuotaExceededError",
	"ProducerBlockedQuotaExceededException", "ChecksumError", "UnsupportedVersionError", "TopicNotFound",
	"SubscriptionNotFound", "ConsumerNotFound", "TooManyRequests", "TopicTerminatedError", "ProducerBusy",
	"InvalidTopicName", "IncompatibleSchema", "ConsumerAssignError", "TransactionCoordinatorNotFound",
	"InvalidTxnStatus", "NotAllowedError", "TransactionConflict", "TransactionNotFound", "ProducerFenced"}

func (e *ServerError) Error() string {
	name := "error " + strconv.Itoa(int(e.Code))
	if e.Code >= 0 && int(e.Code) < len(serverErrorNames) {
		name = serverErrorNames[e.Code]
	}
	if e.Message == "" {
		return "pulsar: " + name
	}
	return "pulsar: " + name + ": " + e.Message
}

// Is matches a ServerError of the same code, as the Err values are.
func (e *ServerError) Is(target error) bool {
	t, ok := target.(*ServerError)
	return ok && t.Code == e.Code
}

// The broker's errors the client and its callers tell apart.
var (
	ErrAuthentication = &ServerError{Code: 3}
	ErrAuthorization  = &ServerError{Code: 4}
	ErrChecksum       = &ServerError{Code: 9}
	ErrTopicNotFound  = &ServerError{Code: 11}
	ErrProducerBusy   = &ServerError{Code: 16}
)

// The client's own errors.
var (
	// ErrClosed is a connection, producer or consumer closed, by the client
	// or the broker; what was waiting on it fails with it.
	ErrClosed = errors.New("pulsar: closed")
	// ErrTooLarge is a message, or a batch of them, larger than the broker
	// takes.
	ErrTooLarge = errors.New("pulsar: larger than the broker's max message size")
	// ErrEndOfTopic is a terminated topic's consumer that has read it all.
	ErrEndOfTopic = errors.New("pulsar: the topic was terminated and is read to its end")
	// ErrUnsupported is a message the client cannot read: encrypted or
	// chunked.
	ErrUnsupported = errors.New("pulsar: unsupported message")
	// ErrFrame is a frame that breaks the protocol, or one larger than
	// the bound.
	ErrFrame = errors.New("pulsar: a malformed frame")
)
