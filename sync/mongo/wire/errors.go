package mongowire

import (
	"errors"
	"fmt"
	"slices"
)

// CommandError is a command's answer with ok: 0.
type CommandError struct {
	Code     int32
	CodeName string
	Message  string
	Labels   []string
	// Reply is the whole answer.
	Reply Raw
}

func (e *CommandError) Error() string {
	if e.CodeName != "" {
		return fmt.Sprintf("mongowire: %s (%d %s)", e.Message, e.Code, e.CodeName)
	}
	return fmt.Sprintf("mongowire: %s (%d)", e.Message, e.Code)
}

// HasLabel reports whether the server labelled the error l, as
// "RetryableWriteError" or "TransientTransactionError".
func (e *CommandError) HasLabel(l string) bool { return slices.Contains(e.Labels, l) }

// commandError reads reply's error when its ok is not 1, nil otherwise.
func commandError(reply Raw) error {
	if ok, found := reply.Lookup("ok"); found && ok.Truthy() {
		return nil
	}
	e := &CommandError{Reply: reply}
	if v, ok := reply.Lookup("code"); ok {
		n, _ := v.Int()
		e.Code = int32(n)
	}
	if v, ok := reply.Lookup("codeName"); ok {
		e.CodeName, _ = v.StringValue()
	}
	if v, ok := reply.Lookup("errmsg"); ok {
		e.Message, _ = v.StringValue()
	}
	if e.Message == "" {
		e.Message = "command failed"
	}
	if v, ok := reply.Lookup("errorLabels"); ok {
		for _, l := range v.Array() {
			if s, ok := l.StringValue(); ok {
				e.Labels = append(e.Labels, s)
			}
		}
	}
	return e
}

// The codes of errors that say the server is not, or no longer, the
// primary, or is shutting down: the command was not run, or was cut, and
// another primary is to be found.
var notPrimaryCodes = []int32{
	10107, // NotWritablePrimary
	13435, // NotPrimaryNoSecondaryOk
	13436, // NotPrimaryOrSecondary
	189,   // PrimarySteppedDown
	91,    // ShutdownInProgress
	11600, // InterruptedAtShutdown
	11602, // InterruptedDueToReplStateChange
	10058, // LegacyNotPrimary
}

// IsNotPrimary reports whether err says the server is not the primary
// (any longer).
func IsNotPrimary(err error) bool {
	var ce *CommandError
	return errors.As(err, &ce) && slices.Contains(notPrimaryCodes, ce.Code)
}

// NetError is an error of a connection: dialing, TLS, reading or writing,
// or a reply that cannot be read. The connection is closed.
type NetError struct {
	Addr string
	Err  error
}

func (e *NetError) Error() string { return "mongowire: " + e.Addr + ": " + e.Err.Error() }
func (e *NetError) Unwrap() error { return e.Err }

// IsNetError reports whether err is a connection's error.
func IsNetError(err error) bool {
	var ne *NetError
	return errors.As(err, &ne)
}
