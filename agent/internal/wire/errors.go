package wire

import (
	"encoding/json"
	"errors"

	"github.com/lesismal/fit/agent/internal/model"
)

// Local failures of a session or stream (never sent on the wire as such).
// 会话或流的本地失败（不会原样发到线上）。
var (
	ErrClosed      = errors.New("wire: session closed")
	ErrCanceled    = errors.New("wire: stream canceled")
	ErrStreamIdle  = errors.New("wire: stream idle timeout")
	ErrStreamEnded = errors.New("wire: stream already ended")
	ErrFlowControl = errors.New("wire: peer exceeded stream window")
	ErrProtocol    = errors.New("wire: protocol violation")
)

// Error is a business error carried by a response or stream-end with FlagError.
// Error 是带 FlagError 的 response 或 stream-end 携带的业务错误。
type Error struct {
	Code int    `json:"code"`
	Msg  string `json:"message"`
}

func (e *Error) Error() string { return e.Msg }

// toError converts a handler error for the wire: an *Error keeps its code, anything else becomes CodeInternal.
// toError 把处理函数的错误转换为线上格式：*Error 保留错误码，其他错误记为 CodeInternal。
func toError(err error) *Error {
	var e *Error
	if errors.As(err, &e) {
		return e
	}
	return &Error{Code: model.CodeInternal, Msg: err.Error()}
}

func encodeError(err error) []byte {
	b, _ := json.Marshal(toError(err))
	return b
}

func decodeError(p []byte) error {
	e := &Error{}
	if json.Unmarshal(p, e) != nil || e.Msg == "" {
		return &Error{Code: model.CodeInternal, Msg: "wire: malformed error payload"}
	}
	return e
}
