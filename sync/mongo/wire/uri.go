package mongowire

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// ParseURI reads a connection string, mongodb://[user:password@]host1[:port1][,host2...][/[authdb]][?options],
// into Options. The options read are replicaSet, authSource,
// authMechanism (SCRAM-SHA-256 only), tls (or ssl), tlsCAFile,
// tlsCertificateKeyFile, connectTimeoutMS, socketTimeoutMS,
// serverSelectionTimeoutMS, maxPoolSize and appName; others are refused,
// as tlsInsecure and tlsAllowInvalidCertificates are, so that nothing is
// silently left out. mongodb+srv is not read.
func ParseURI(uri string) (Options, error) {
	var o Options
	rest, ok := strings.CutPrefix(uri, "mongodb://")
	if !ok {
		return o, errors.New("mongowire: a connection string starts with mongodb://")
	}
	var query string
	rest, query, _ = strings.Cut(rest, "?")
	rest, authDB, _ := strings.Cut(rest, "/")
	if i := strings.LastIndexByte(rest, '@'); i >= 0 {
		user, pass, _ := strings.Cut(rest[:i], ":")
		var err error
		if o.Username, err = url.PathUnescape(user); err != nil {
			return o, fmt.Errorf("mongowire: the connection string's user: %w", err)
		}
		if o.Password, err = url.PathUnescape(pass); err != nil {
			return o, fmt.Errorf("mongowire: the connection string's password: %w", err)
		}
		rest = rest[i+1:]
	}
	for _, h := range strings.Split(rest, ",") {
		if h == "" {
			return o, errors.New("mongowire: an empty host in the connection string")
		}
		o.Hosts = append(o.Hosts, withPort(h))
	}
	if authDB != "" {
		var err error
		if o.AuthSource, err = url.PathUnescape(authDB); err != nil {
			return o, err
		}
	}
	q, err := url.ParseQuery(query)
	if err != nil {
		return o, fmt.Errorf("mongowire: the connection string's options: %w", err)
	}
	var useTLS bool
	var caFile, certFile string
	ms := func(v string) (time.Duration, error) {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("mongowire: %q milliseconds", v)
		}
		return time.Duration(n) * time.Millisecond, nil
	}
	for k, vs := range q {
		v := vs[len(vs)-1]
		var err error
		switch strings.ToLower(k) {
		case "replicaset":
			o.ReplicaSet = v
		case "authsource":
			o.AuthSource = v
		case "authmechanism":
			if v != "SCRAM-SHA-256" {
				err = fmt.Errorf("mongowire: authMechanism %s: only SCRAM-SHA-256 is spoken", v)
			}
		case "tls", "ssl":
			useTLS = v == "true"
		case "tlscafile":
			caFile, useTLS = v, true
		case "tlscertificatekeyfile":
			certFile, useTLS = v, true
		case "connecttimeoutms":
			o.ConnectTimeout, err = ms(v)
		case "sockettimeoutms":
			o.Timeout, err = ms(v)
		case "serverselectiontimeoutms":
			o.ServerSelectionTimeout, err = ms(v)
		case "maxpoolsize":
			o.MaxConns, err = strconv.Atoi(v)
		case "appname":
			o.AppName = v
		default:
			err = fmt.Errorf("mongowire: the connection string's option %s is not read", k)
		}
		if err != nil {
			return o, err
		}
	}
	if useTLS {
		o.TLS = &tls.Config{MinVersion: tls.VersionTLS12}
		if caFile != "" {
			pem, err := os.ReadFile(caFile)
			if err != nil {
				return o, err
			}
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(pem) {
				return o, fmt.Errorf("mongowire: no certificate in %s", caFile)
			}
			o.TLS.RootCAs = pool
		}
		if certFile != "" {
			// MongoDB's file holds the certificate and its key.
			cert, err := tls.LoadX509KeyPair(certFile, certFile)
			if err != nil {
				return o, err
			}
			o.TLS.Certificates = []tls.Certificate{cert}
		}
	}
	return o, nil
}
