package config

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestValidateMore: errors each found among the others a config has.
func TestValidateMore(t *testing.T) {
	const web = `"listeners": [{"name": "web", "address": ":80"}]`
	for _, tt := range []struct {
		name, doc, want string
	}{
		{"docker unix without a path", `{` + web + `, "providers": [{"docker": {"endpoint": "unix://"}}]}`,
			`providers[0].docker.endpoint: "unix://" has no path`},
		{"etcd provider password without username", `{` + web + `, "providers": [{"etcd": {"password_file": "p"}}]}`,
			`providers[0].etcd.password_file: password_file needs username`},
		{"kubernetes address", `{` + web + `, "providers": [{"kubernetes": {"addresses": ["*.a.example"]}}]}`,
			`providers[0].kubernetes.addresses[0]: "*.a.example" is not an IP address or host name`},
		{"redis cert without tls", `{` + web + `, "acme": {"shared_storage": {"redis": {"cert_file": "c", "key_file": "k"}}}}`,
			`acme.shared_storage.redis.cert_file: cert_file needs tls`},
		{"etcd storage password without username", `{` + web + `, "acme": {"shared_storage": {"etcd": {"password_file": "p"}}}}`,
			`acme.shared_storage.etcd.password_file: password_file needs username`},
		{"etcd storage username without password", `{` + web + `, "acme": {"shared_storage": {"etcd": {"username": "u"}}}}`,
			`acme.shared_storage.etcd.username: username needs password_file`},
		{"empty tls, HTTPS by default off", `{"auto_https": {"disable": true}, "listeners": [{"name": "web", "address": ":443", "tls": {}}]}`,
			`listeners[0].tls: a certificate, a name for acme or on_demand is needed`},
		{"route name", `{` + web + `, "routes": [{"name": "-r", "handler": {"module": "respond"}}]}`,
			`routes[0].name: "-r" is not a valid name`},
		{"middleware without a module", `{` + web + `, "routes": [{"middleware": [{"module": ""}], "handler": {"module": "respond"}}]}`,
			`routes[0].middleware[0].module: the module name is empty`},
		{"acme directory", `{` + web + `, "acme": {"directory": "ftp://x"}}`,
			`acme.directory: "ftp://x" is not an http or https URL`},
		{"service name", `{"listeners": [{"name": "raw", "address": ":1", "protocols": ["tcp"]}], "upstreams": [{"name": "u", "addresses": ["a:1"]}],
  "services": [{"name": "-s", "listeners": ["raw"], "upstream": "u"}]}`,
			`services[0].name: "-s" is not a valid name`},
		{"udp listener twice", `{"listeners": [{"name": "dns", "address": ":53", "protocols": ["udp"]}], "upstreams": [{"name": "u", "addresses": ["a:1"]}],
  "services": [{"name": "a", "listeners": ["dns"], "upstream": "u"}, {"name": "b", "listeners": ["dns"], "upstream": "u"}]}`,
			`services[1].listeners[0]: listener "dns" is already service "a"'s`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load([]byte(tt.doc))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Load: %v\nwant %s", err, tt.want)
			}
		})
	}
	// An endpoint left out is the default.
	if _, err := Load([]byte(`{` + web + `, "providers": [{"consul": {}}]}`)); err != nil {
		t.Errorf("consul's default endpoint: %v", err)
	}
	if errs := (&ForwardProxy{Clients: []string{"bogus"}}).Check(); len(errs) != 1 || !strings.Contains(errs[0].Error(), `"bogus" is not an IP address or CIDR range`) {
		t.Errorf("ForwardProxy.Check: %v", errs)
	}
	if errs := (&ForwardProxy{Clients: []string{"10.0.0.0/8"}}).Check(); len(errs) != 0 {
		t.Errorf("ForwardProxy.Check: %v", errs)
	}
}

// TestSettings: what settings return, left out and set.
func TestSettings(t *testing.T) {
	d := Duration(3 * time.Second)
	check := func(name string, got, want any) {
		t.Helper()
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %v, want %v", name, got, want)
		}
	}
	var zd DockerProvider
	check("docker refresh", zd.Refresh(), DefaultDockerRefreshInterval.Std())
	check("docker timeout", (&DockerProvider{RequestTimeout: d}).Timeout(), d.Std())
	check("consul url", (&ConsulProvider{}).URL(), DefaultConsulEndpoint)
	check("consul kv url", (&ConsulKVProvider{}).URL(), DefaultConsulEndpoint)
	check("etcd urls", (&EtcdProvider{}).URLs(), []string{DefaultEtcdEndpoint})
	check("kubernetes timeout", (&KubernetesProvider{RequestTimeout: d}).Timeout(), d.Std())
	check("kind", (&Provider{}).Kind(), "")

	check("storage prefix", (&SharedStorage{}).KeyPrefix(), DefaultSharedStoragePrefix)
	check("storage ttl", (&SharedStorage{}).TTL(), DefaultSharedStorageLockTTL.Std())
	check("storage timeout", (&SharedStorage{RequestTimeout: d}).Timeout(), d.Std())
	check("redis addr", (&RedisStorage{}).Addr(), DefaultRedisAddress)
	check("etcd storage urls", (&EtcdStorage{}).URLs(), []string{DefaultEtcdEndpoint})
	check("consul storage url", (&ConsulStorage{}).URL(), DefaultConsulEndpoint)

	check("telemetry url", (&Telemetry{}).URL(), DefaultTelemetryEndpoint)
	check("telemetry timeout", (&Telemetry{Timeout: d}).ExportTimeout(), d.Std())
	check("metrics every", (&MetricsExport{}).Every(), DefaultMetricsInterval.Std())

	check("challenges", (&ACME{Challenges: []string{ChallengeHTTP01}}).ChallengeTypes(), []string{ChallengeHTTP01})
	check("warnings", (&Config{}).Warnings(), Errors(nil))
}

func TestLoadFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fit.json")
	if _, err := LoadFile(path); err == nil {
		t.Error("LoadFile of a missing file succeeded")
	}
	if err := os.WriteFile(path, []byte(`{"listeners": [{"name": "web", "address": ":80"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if c, err := LoadFile(path); err != nil || len(c.Listeners) != 1 {
		t.Fatalf("LoadFile: %v", err)
	}
}

// TestFieldsPanics: a tag Fields cannot read is a mistake in the type, and
// panics.
func TestFieldsPanics(t *testing.T) {
	for _, tt := range []struct {
		name string
		typ  reflect.Type
	}{
		{"default", reflect.TypeFor[struct {
			A int `json:"a" default:"{"`
		}]()},
		{"fit option", reflect.TypeFor[struct {
			A int `json:"a" fit:"bogus"`
		}]()},
		{"self", reflect.TypeFor[struct {
			A int `json:"-" fit:"self"`
		}]()},
		{"min", reflect.TypeFor[struct {
			A int `json:"a" min:"x"`
		}]()},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: no panic", tt.name)
				}
			}()
			Fields(tt.typ)
		}()
	}
	fs := Fields(reflect.TypeFor[struct {
		Plain int
		Gone  int `json:"-"`
		lower int
	}]())
	if len(fs) != 1 || fs[0].Name != "Plain" {
		t.Errorf("fields: %+v", fs)
	}
}

type decodeKinds struct {
	Map   map[string]int `json:"map"`
	Int   int            `json:"int"`
	Uint  uint           `json:"uint"`
	Float float32        `json:"float"`
	Ptr   *struct{}      `json:"ptr"`
	Ints  []int          `json:"ints"`
}

func TestDecodeKinds(t *testing.T) {
	v, err := ParseValue([]byte(`{"map": 1, "int": "x", "uint": "x", "float": "x", "ints": [1, "x"]}`), "p")
	if err != nil {
		t.Fatal(err)
	}
	var k decodeKinds
	err = v.Decode(&k)
	errs, _ := err.(Errors)
	var got []string
	for _, e := range errs {
		got = append(got, e.Path)
	}
	if !slices.Equal(got, []string{"p.map", "p.int", "p.uint", "p.float", "p.ints[1]"}) {
		t.Errorf("errors at %q: %v", got, err)
	}

	v, _ = ParseValue([]byte(`{"float": 1e40}`), "p")
	if err := v.Decode(&k); err == nil || !strings.Contains(err.Error(), "out of range for float32") {
		t.Errorf("float32 overflow: %v", err)
	}
	k.Ptr = &struct{}{}
	v, _ = ParseValue([]byte(`{"ptr": null, "ints": [1, 2], "map": {"a": 1}}`), "p")
	if err := v.Decode(&k); err != nil || k.Ptr != nil || !slices.Equal(k.Ints, []int{1, 2}) || k.Map["a"] != 1 {
		t.Errorf("null pointer: %+v %v", k, err)
	}

	// A required field missing from an absent Value with nothing to anchor
	// it has no position.
	var req struct {
		A int `json:"a" fit:"required"`
	}
	if err := (Value{}).Decode(&req); err == nil || strings.Contains(err.Error(), ":0:") {
		t.Errorf("absent value: %v", err)
	}

	for _, tt := range []struct {
		name string
		dst  any
	}{
		{"not a pointer", decodeKinds{}},
		{"map key", &struct {
			M map[int]int `json:"m"`
		}{}},
		{"unsupported type", &struct {
			C complex64 `json:"m"`
		}{}},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Errorf("%s: no panic", tt.name)
				}
			}()
			v, _ := ParseValue([]byte(`{"m": {"1": 1}}`), "p")
			if tt.name == "unsupported type" {
				v, _ = ParseValue([]byte(`{"m": 1}`), "p")
			}
			_ = v.Decode(tt.dst)
		}()
	}
}

func TestLogConfig(t *testing.T) {
	routes := `"listeners": [{"name": "web", "address": ":8080"}], "routes": [{"handler": {"module": "respond"}}]`
	c, err := Load([]byte(`{"log": {"level": "warn", "levels": {"tls": "debug"}, "format": "json"}, "access_log": {"level": "error"}, ` + routes + `}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Log.LevelOf(LoggerTLS) != LevelDebug || c.Log.LevelOf(LoggerEngine) != LevelWarn || (*Log)(nil).LevelOf(LoggerTLS) != LevelInfo ||
		(&Log{}).LevelOf(LoggerTLS) != LevelInfo {
		t.Errorf("levels of %+v", c.Log)
	}
	if c.AccessLog.MinStatus() != 500 || (&AccessLog{Level: "warn"}).MinStatus() != 400 || (&AccessLog{}).MinStatus() != 0 {
		t.Errorf("access log's least status")
	}
	for doc, want := range map[string]string{
		`{"log": {"levels": {"http": "info"}}, ` + routes + `}`: `log.levels.http: no logger is named "http"`,
		`{"log": {"levels": {"tls": "loud"}}, ` + routes + `}`:  `log.levels.tls: "loud" is not a level`,
		`{"log": {"level": "trace"}, ` + routes + `}`:           `log.level`,
		`{"log": {"format": "xml"}, ` + routes + `}`:            `log.format`,
		`{"access_log": {"level": "debug"}, ` + routes + `}`:    `access_log.level`,
	} {
		_, err := Load([]byte(doc))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", doc, err, want)
		}
	}
}
