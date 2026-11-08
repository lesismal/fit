package syncmongo

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// TestConnParamsShared checks that the source and the sink declare
// ConnParams' fields first, with its names, types and tags, and copy each
// of them into it.
func TestConnParamsShared(t *testing.T) {
	cp := reflect.TypeFor[ConnParams]()
	for _, typ := range []reflect.Type{reflect.TypeFor[Source](), reflect.TypeFor[Sink]()} {
		for i := range cp.NumField() {
			want, got := cp.Field(i), typ.Field(i)
			if got.Name != want.Name || got.Type != want.Type || got.Tag != want.Tag {
				t.Errorf("%s field %d is %s %s `%s`, want ConnParams' %s", typ, i, got.Name, got.Type, got.Tag, want.Name)
			}
		}
	}
	// Each field set to a value of its own: conn must carry them all.
	src, snk := reflect.New(reflect.TypeFor[Source]()), reflect.New(reflect.TypeFor[Sink]())
	for i := range cp.NumField() {
		for _, v := range []reflect.Value{src.Elem(), snk.Elem()} {
			f := v.Field(i)
			switch f.Kind() {
			case reflect.String:
				f.SetString(cp.Field(i).Name)
			case reflect.Bool:
				f.SetBool(true)
			case reflect.Int, reflect.Int64:
				f.SetInt(int64(i + 1))
			case reflect.Slice:
				f.Set(reflect.ValueOf([]string{cp.Field(i).Name}))
			}
		}
	}
	a, b := src.Interface().(*Source).conn(), snk.Interface().(*Sink).conn()
	av := reflect.ValueOf(a)
	for i := range cp.NumField() {
		if av.Field(i).IsZero() {
			t.Errorf("conn leaves %s out", cp.Field(i).Name)
		}
	}
	if !reflect.DeepEqual(a, b) {
		t.Errorf("source %+v, sink %+v", a, b)
	}
}

// TestConnParamsSource checks that the source takes the sink's connection
// params: hosts, username, TLS and the timeouts.
func TestConnParamsSource(t *testing.T) {
	cert, key, _ := sinkCert(t)
	t.Setenv("FIT_TEST_MONGO_PW", "secret")
	s, err := srcLoad(t, map[string]any{"hosts": []string{"h1", "h2:27018"}, "replica_set": "rs", "username": "u",
		"password_env": "FIT_TEST_MONGO_PW", "auth_source": "users", "tls": true, "ca_file": cert, "cert_file": cert,
		"key_file": key, "server_name": "db", "connect_timeout": "2s", "timeout": "5s", "max_conns": 8})
	if err != nil {
		t.Fatal(err)
	}
	o := s.client.Options()
	if len(o.Hosts) != 2 || o.ReplicaSet != "rs" || o.Username != "u" || o.Password != "secret" || o.AuthSource != "users" ||
		o.TLS == nil || o.TLS.ServerName != "db" || o.TLS.RootCAs == nil || len(o.TLS.Certificates) != 1 ||
		o.ConnectTimeout != 2*time.Second || o.Timeout != 5*time.Second || o.MaxConns != 8 || o.AppName != "fit-sync" {
		t.Errorf("options %+v", o)
	}
	// The connection string's socketTimeoutMS stands when timeout is left out.
	if s, err = srcLoad(t, map[string]any{"uri": "mongodb://h/?socketTimeoutMS=7000"}); err != nil {
		t.Fatal(err)
	}
	if o := s.client.Options(); o.Timeout != 7*time.Second {
		t.Errorf("timeout %v", o.Timeout)
	}
	if _, err := srcLoad(t, map[string]any{"uri": "mongodb://h", "server_name": "db"}); err == nil || !strings.Contains(err.Error(), "needs tls") {
		t.Errorf("server_name without tls: %v", err)
	}
}
