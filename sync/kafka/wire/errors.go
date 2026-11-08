package kafkawire

import (
	"errors"
	"fmt"
)

// Error is an error code a broker answered with.
type Error int16

// The error codes this package acts on; the others print by their names
// and numbers and are not retried.
const (
	ErrUnknownServerError           Error = -1
	ErrOffsetOutOfRange             Error = 1
	ErrCorruptMessage               Error = 2
	ErrUnknownTopicOrPartition      Error = 3
	ErrLeaderNotAvailable           Error = 5
	ErrNotLeaderOrFollower          Error = 6
	ErrRequestTimedOut              Error = 7
	ErrReplicaNotAvailable          Error = 9
	ErrMessageTooLarge              Error = 10
	ErrNetworkException             Error = 13
	ErrNotEnoughReplicas            Error = 19
	ErrNotEnoughReplicasAfterAppend Error = 20
	ErrTopicAuthorizationFailed     Error = 29
	ErrClusterAuthorizationFailed   Error = 31
	ErrUnsupportedSASLMechanism     Error = 33
	ErrIllegalSASLState             Error = 34
	ErrUnsupportedVersion           Error = 35
	ErrTopicAlreadyExists           Error = 36
	ErrNotController                Error = 41
	ErrKafkaStorageError            Error = 56
	ErrSASLAuthenticationFailed     Error = 58
	ErrFencedLeaderEpoch            Error = 74
	ErrUnknownLeaderEpoch           Error = 75
	ErrUnsupportedCompressionType   Error = 76
	ErrOffsetNotAvailable           Error = 78
	ErrThrottlingQuotaExceeded      Error = 89
	ErrUnknownTopicID               Error = 100
	ErrInconsistentTopicID          Error = 103
)

var errorNames = map[Error]string{
	-1: "UNKNOWN_SERVER_ERROR", 1: "OFFSET_OUT_OF_RANGE", 2: "CORRUPT_MESSAGE",
	3: "UNKNOWN_TOPIC_OR_PARTITION", 4: "INVALID_FETCH_SIZE", 5: "LEADER_NOT_AVAILABLE",
	6: "NOT_LEADER_OR_FOLLOWER", 7: "REQUEST_TIMED_OUT", 8: "BROKER_NOT_AVAILABLE",
	9: "REPLICA_NOT_AVAILABLE", 10: "MESSAGE_TOO_LARGE", 12: "OFFSET_METADATA_TOO_LARGE",
	13: "NETWORK_EXCEPTION", 17: "INVALID_TOPIC_EXCEPTION", 18: "RECORD_LIST_TOO_LARGE",
	19: "NOT_ENOUGH_REPLICAS", 20: "NOT_ENOUGH_REPLICAS_AFTER_APPEND", 21: "INVALID_REQUIRED_ACKS",
	29: "TOPIC_AUTHORIZATION_FAILED", 31: "CLUSTER_AUTHORIZATION_FAILED",
	33: "UNSUPPORTED_SASL_MECHANISM", 34: "ILLEGAL_SASL_STATE", 35: "UNSUPPORTED_VERSION",
	36: "TOPIC_ALREADY_EXISTS", 37: "INVALID_PARTITIONS", 38: "INVALID_REPLICATION_FACTOR",
	39: "INVALID_REPLICA_ASSIGNMENT", 40: "INVALID_CONFIG", 41: "NOT_CONTROLLER",
	42: "INVALID_REQUEST", 43: "UNSUPPORTED_FOR_MESSAGE_FORMAT", 44: "POLICY_VIOLATION",
	45: "OUT_OF_ORDER_SEQUENCE_NUMBER", 46: "DUPLICATE_SEQUENCE_NUMBER",
	47: "INVALID_PRODUCER_EPOCH", 56: "KAFKA_STORAGE_ERROR", 58: "SASL_AUTHENTICATION_FAILED",
	74: "FENCED_LEADER_EPOCH", 75: "UNKNOWN_LEADER_EPOCH", 76: "UNSUPPORTED_COMPRESSION_TYPE",
	78: "OFFSET_NOT_AVAILABLE", 87: "INVALID_RECORD", 89: "THROTTLING_QUOTA_EXCEEDED",
	100: "UNKNOWN_TOPIC_ID", 103: "INCONSISTENT_TOPIC_ID",
}

func (e Error) Error() string {
	if n, ok := errorNames[e]; ok {
		return fmt.Sprintf("kafka: %s (%d)", n, int16(e))
	}
	return fmt.Sprintf("kafka: error %d", int16(e))
}

// Retriable reports whether the same request may succeed later: a
// partition between leaders, a broker timing out or short of replicas.
func (e Error) Retriable() bool {
	switch e {
	case ErrCorruptMessage, ErrUnknownTopicOrPartition, ErrLeaderNotAvailable,
		ErrNotLeaderOrFollower, ErrRequestTimedOut, ErrReplicaNotAvailable,
		ErrNetworkException, ErrNotEnoughReplicas, ErrNotEnoughReplicasAfterAppend,
		ErrNotController, ErrKafkaStorageError, ErrFencedLeaderEpoch,
		ErrUnknownLeaderEpoch, ErrOffsetNotAvailable, ErrThrottlingQuotaExceeded,
		ErrUnknownTopicID, ErrInconsistentTopicID:
		return true
	}
	return false
}

// staleMetadata reports whether the error says the client's idea of a
// partition's leader is out of date, so metadata is read again before the
// retry.
func (e Error) staleMetadata() bool {
	switch e {
	case ErrUnknownTopicOrPartition, ErrLeaderNotAvailable, ErrNotLeaderOrFollower,
		ErrReplicaNotAvailable, ErrNetworkException, ErrKafkaStorageError,
		ErrFencedLeaderEpoch, ErrUnknownLeaderEpoch, ErrUnknownTopicID,
		ErrInconsistentTopicID, ErrNotController:
		return true
	}
	return false
}

// IsRetriable reports whether err, from any call of the client, may go away
// when the call is made again: a retriable error code, or a connection that
// failed. A call already retries these until its context or its attempts
// run out, so a caller sees one only then.
func IsRetriable(err error) bool {
	var e Error
	if errors.As(err, &e) {
		return e.Retriable()
	}
	var ce *ConnError
	return errors.As(err, &ce)
}

// ConnError is a connection to a broker that could not be made, or failed
// while a request was on it.
type ConnError struct {
	Addr string
	Err  error
}

func (e *ConnError) Error() string { return "kafka: broker " + e.Addr + ": " + e.Err.Error() }
func (e *ConnError) Unwrap() error { return e.Err }
