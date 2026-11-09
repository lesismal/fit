package fibrest

import (
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

// Connection is what a module's params say of its server, as the doris,
// loki and otlp_logs sinks take them: URLs tried in turn, credentials from
// the environment, TLS, a timeout (60s when 0), and headers of the
// module's own, checked as HeaderFields checks them under the param
// headerField.
type Connection struct {
	URLs                                  []string
	Username, PasswordEnv, BearerTokenEnv string
	CAFile, CertFile, KeyFile, ServerName string
	Timeout                               config.Duration
	Headers                               map[string]string
	HeaderField                           string
}

// DefaultModuleTimeout is a Connection's timeout when it sets none.
const DefaultModuleTimeout = 60 * time.Second

// Connect makes the client of a module's Connection, on its engine; the
// errors are the module's config errors, by param. Nothing is sent until
// the first request.
func Connect(ctx *module.Context, p Connection) (*Client, config.Errors) {
	var errs config.Errors
	add := func(fes []FieldError) {
		for _, fe := range fes {
			errs = append(errs, ctx.Params.Field(fe.Field).Errorf("%v", fe.Err))
		}
	}
	if len(p.URLs) == 0 {
		return nil, config.Errors{ctx.Params.Field("urls").Errorf("no URLs")}
	}
	header, fes := HeaderFields(p.HeaderField, p.Headers)
	add(fes)
	auth, fes := Authorization(p.Username, p.PasswordEnv, p.BearerTokenEnv)
	add(fes)
	if auth != "" {
		header.Set("Authorization", auth)
	}
	timeout := p.Timeout.Std()
	if timeout <= 0 {
		timeout = DefaultModuleTimeout
	}
	c, fes := New(ctx.Engine(), Params{URLs: p.URLs, CAFile: p.CAFile, CertFile: p.CertFile, KeyFile: p.KeyFile,
		ServerName: p.ServerName, Timeout: timeout, Header: header})
	add(fes)
	return c, errs
}
