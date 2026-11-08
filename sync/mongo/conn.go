// Package syncmongo is sync's MongoDB: the source mongo, which reads change
// streams from a resume token after a snapshot by _id, and the sink mongo.
// Both speak to a replica set's primary (or a mongos) through
// sync/mongo/wire, a blocking client whose connections are used by the
// pipeline's own goroutines, never by a fib worker. See
// docs/decisions/036-sync.md.
//
// This file holds what the source and the sink share: the params for
// reaching the server, which each declares as its own, with the same names
// and docs:
//
//	URI         string          `json:"uri" fit:"required" doc:"..."`
//	PasswordEnv string          `json:"password_env,omitempty" doc:"..."`
//	Timeout     config.Duration `json:"timeout,omitempty" default:"\"60s\"" doc:"..."`
//
// (the docs are the source's, in source.go), and hands to NewClient.
package syncmongo

import (
	"context"
	"errors"
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	mongowire "github.com/lesismal/fit/sync/mongo/wire"
)

// DefaultTimeout bounds a command when the params' timeout is left out.
const DefaultTimeout = 60 * time.Second

// ConnParams are a connector's params for reaching MongoDB.
type ConnParams struct {
	// URI is a mongodb:// connection string, as mongowire.ParseURI reads
	// it, without a password.
	URI string
	// PasswordEnv names the environment variable holding the password of
	// URI's user.
	PasswordEnv string
	// Timeout bounds each command, DefaultTimeout when 0.
	Timeout config.Duration
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// NewClient checks p and makes a client of it, reading the password from
// the environment and the TLS files the URI names; nothing is sent until
// the client is first used. Errors name the params' fields.
func NewClient(ctx *module.Context, p ConnParams) (*mongowire.Client, config.Errors) {
	var errs config.Errors
	add := func(field, format string, args ...any) {
		errs = append(errs, ctx.Params.Field(field).Errorf(format, args...))
	}
	o, err := mongowire.ParseURI(p.URI)
	if err != nil {
		add("uri", "%v", err)
		return nil, errs
	}
	if o.Password != "" {
		add("uri", "the connection string holds a password: name the environment variable holding it with password_env")
	}
	switch {
	case p.PasswordEnv != "" && !envName.MatchString(p.PasswordEnv):
		add("password_env", "%q is not an environment variable's name", p.PasswordEnv)
	case p.PasswordEnv != "":
		if o.Password = os.Getenv(p.PasswordEnv); o.Password == "" {
			add("password_env", "the environment variable %s is not set, or empty", p.PasswordEnv)
		}
		if o.Username == "" {
			add("password_env", "password_env needs a user in uri, as mongodb://user@host")
		}
	case o.Username != "":
		add("uri", "the connection string's user needs password_env")
	}
	o.Timeout = DefaultTimeout
	if p.Timeout > 0 {
		o.Timeout = p.Timeout.Std()
	}
	if o.AppName == "" {
		o.AppName = "fit-sync"
	}
	if len(errs) > 0 {
		return nil, errs
	}
	c, err := mongowire.New(o)
	if err != nil {
		add("uri", "%v", err)
		return nil, errs
	}
	return c, nil
}

// Ping checks that c reaches a primary and authenticates, for a
// connector's Test.
func Ping(ctx context.Context, c *mongowire.Client) error {
	if c == nil {
		return errors.New("mongo: not provisioned")
	}
	if _, err := c.Run(ctx, "admin", mongowire.Doc("ping", 1)); err != nil {
		return fmt.Errorf("mongo: %w", err)
	}
	return nil
}
