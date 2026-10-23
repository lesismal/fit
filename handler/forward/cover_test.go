package forward_test

import (
	"strings"
	"testing"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/runtime"
)

// TestProvisionErrors refuses params whose rules do not parse, with the
// field that is wrong.
func TestProvisionErrors(t *testing.T) {
	for params, want := range map[string]string{
		`{"clients": ["nope"]}`:                        `"nope" is not an IP address or CIDR range`,
		`{"clients": ["127.0.0.1"], "allow": ["a b"]}`: `"a b" is not a host name`,
	} {
		c, err := config.Load([]byte(`{
  "listeners": [{"name": "web", "address": "127.0.0.1:0"}],
  "routes": [{"handler": {"module": "forward_proxy", "params": ` + params + `}}]
}`))
		if err == nil {
			_, err = runtime.Compile(c, runtime.Env{})
		}
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", params, err, want)
		}
	}
}
