package proxy

import (
	"errors"
	stdhttp "net/http"
	"strconv"
	"strings"
	"time"

	fibhttp "github.com/lesismal/fib/http"
)

// A gRPC call is an HTTP/2 or HTTP/3 POST whose Content-Type is
// application/grpc or one of its subtypes, and the proxy passes it on as it
// does any request: its messages stream both ways as they come, each side
// paced by the other, and the upstream's trailers, with grpc-status and
// grpc-message, follow its response. A call needs an h2 or h2c upstream.
// What the proxy adds:
//
//   - TE: trailers goes upstream, as gRPC servers ask of their clients;
//   - grpc-timeout bounds the call at the proxy too, as the route's timeout
//     does, and the upstream is told the shorter of the two;
//   - a call the proxy cannot complete is answered as gRPC answers: a
//     trailers-only response, status 200 with grpc-status and grpc-message,
//     before its response began, and those trailers on a response cut short,
//     rather than an HTTP status or a reset its client would not read.
//
// The listener streams a gRPC call's body to the proxy from its header (fib's
// StreamRequestBodyAtHeader), so that a client that waits for an answer to
// its first message is answered.

// errCallDeadline is a call cut short by its grpc-timeout.
var errCallDeadline = errors.New("proxy: the call's grpc-timeout passed")

// gRPC status codes the proxy answers with.
const (
	grpcDeadlineExceeded  = 4
	grpcResourceExhausted = 8
	grpcInternal          = 13
	grpcUnavailable       = 14
)

// isGRPC reports whether r is a gRPC call. listener.streamsAtHeader knows
// them the same way.
func isGRPC(r *stdhttp.Request) bool {
	if r.ProtoMajor < 2 || r.Method != stdhttp.MethodPost {
		return false
	}
	ct := r.Header["Content-Type"]
	return len(ct) == 1 && grpcContentType(ct[0])
}

// grpcContentType reports whether ct is application/grpc or one of its
// subtypes, as application/grpc+proto; application/grpc-web is not.
func grpcContentType(ct string) bool {
	const prefix = "application/grpc"
	if len(ct) < len(prefix) || !strings.EqualFold(ct[:len(prefix)], prefix) {
		return false
	}
	rest := ct[len(prefix):]
	return rest == "" || rest[0] == '+' || rest[0] == ';'
}

// teTrailers is the TE field a call goes upstream with.
var teTrailers = []string{"trailers"}

// grpcTimeout parses a grpc-timeout field's values: at most eight digits and
// a unit.
func grpcTimeout(values []string) (time.Duration, bool) {
	if len(values) != 1 || len(values[0]) < 2 || len(values[0]) > 9 {
		return 0, false
	}
	s := values[0]
	v, err := strconv.ParseInt(s[:len(s)-1], 10, 64)
	if err != nil || v < 0 {
		return 0, false
	}
	var unit time.Duration
	switch s[len(s)-1] {
	case 'n':
		unit = time.Nanosecond
	case 'u':
		unit = time.Microsecond
	case 'm':
		unit = time.Millisecond
	case 'S':
		unit = time.Second
	case 'M':
		unit = time.Minute
	case 'H':
		unit = time.Hour
	default:
		return 0, false
	}
	if v > int64(1<<63-1)/int64(unit) {
		return 1<<63 - 1, true
	}
	return time.Duration(v) * unit, true
}

// encodeGRPCTimeout is d as grpc-timeout carries it, rounded up.
func encodeGRPCTimeout(d time.Duration) string {
	units := [...]struct {
		unit byte
		size time.Duration
	}{{'n', time.Nanosecond}, {'u', time.Microsecond}, {'m', time.Millisecond},
		{'S', time.Second}, {'M', time.Minute}, {'H', time.Hour}}
	const maxValue = 1e8 - 1
	for _, u := range units {
		if v := (d + u.size - 1) / u.size; v <= maxValue {
			return strconv.FormatInt(int64(max(v, 1)), 10) + string(u.unit)
		}
	}
	return strconv.FormatInt(maxValue, 10) + "H"
}

// callTimeout is how long the call x carries may take, and why it would be
// cut short: the route's timeout, or the call's grpc-timeout if it is
// shorter. The upstream is told the one chosen.
func (x *exchange) callTimeout(route time.Duration) (time.Duration, error) {
	d, ok := grpcTimeout(x.c.Request.Header["Grpc-Timeout"])
	if ok && (route <= 0 || d <= route) {
		return d, errCallDeadline
	}
	if route > 0 {
		x.base.Header["Grpc-Timeout"] = []string{encodeGRPCTimeout(route)}
	}
	return route, errRouteTimeout
}

// grpcCode is the gRPC status of a call the proxy answers with an HTTP
// status, as gRPC maps them.
func grpcCode(status int) int {
	switch status {
	case stdhttp.StatusGatewayTimeout, stdhttp.StatusRequestTimeout:
		return grpcDeadlineExceeded
	case stdhttp.StatusRequestEntityTooLarge:
		return grpcResourceExhausted
	case stdhttp.StatusBadRequest:
		return grpcInternal
	}
	return grpcUnavailable
}

// grpcFail answers a call that got no response with status's gRPC status, in
// a trailers-only response.
func (x *exchange) grpcFail(status int) {
	code := grpcCode(status)
	ct := x.c.Request.Header["Content-Type"]
	_ = x.c.WriteResponse(fibhttp.Response{StatusCode: stdhttp.StatusOK, Header: stdhttp.Header{
		"Content-Type": ct,
		"Grpc-Status":  {strconv.Itoa(code)},
		"Grpc-Message": {grpcMessage(code)},
	}})
}

// grpcBroken ends a call whose response failed after it began with
// trailers that say why. The header went with the response.
func (x *exchange) grpcBroken() {
	code := grpcUnavailable
	if x.timedOut() {
		code = grpcDeadlineExceeded
	}
	h := x.c.Header()
	h[stdhttp.TrailerPrefix+"Grpc-Status"] = []string{strconv.Itoa(code)}
	h[stdhttp.TrailerPrefix+"Grpc-Message"] = []string{grpcMessage(code)}
}

// grpcMessage is the grpc-message of a code the proxy answers with.
func grpcMessage(code int) string {
	switch code {
	case grpcDeadlineExceeded:
		return "fit: the call took too long"
	case grpcResourceExhausted:
		return "fit: the request is too large"
	case grpcInternal:
		return "fit: the request was malformed"
	}
	return "fit: the upstream is unavailable"
}
