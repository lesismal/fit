package schema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/lesismal/fit/adapter/fitconf"
	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
)

// Template returns a config template in format, "fit", "yaml" or "json", that
// holds every field of the native config and of every module's params,
// each with its description, type, default and choices in a comment above
// it. A small config is written out, which fit loads as it is: a listener,
// an upstream and a route; every other field is there commented out, with
// its default or an example value, to be uncommented. The JSON has
// comments and trailing commas, which fit takes (see config.Standardize).
// templates/ holds the three, for the modules in modules/standard.
func Template(format string) ([]byte, error) {
	if format != "json" && format != "yaml" && format != "fit" {
		return nil, fmt.Errorf("schema: no template in %q: fit, yaml or json", format)
	}
	t := &templater{}
	root := t.object(reflect.TypeFor[config.Config](), "", nil)
	var buf bytes.Buffer
	w := &tmplWriter{buf: &buf, yaml: format == "yaml", fit: format == "fit"}
	w.head()
	if w.fit {
		buf.WriteString("\n")
		w.fitMembers(root.children, 0, true, true)
		return buf.Bytes(), nil
	}
	indent := 1
	if w.yaml {
		indent = 0
	}
	w.members(root.children, indent)
	if !w.yaml {
		buf.WriteString("}\n")
	}
	return buf.Bytes(), nil
}

// tnode is a member of the template: a field with its comment, and its
// value, a scalar written as JSON, an object's members, or an array's
// elements.
type tnode struct {
	name     string
	doc      []string
	active   bool
	scalar   string
	children []*tnode // an object's members
	elems    []*tnode // an array's elements, each an object
	isArray  bool
	isObject bool
}

type templater struct{}

// active are the fields written out, by path; the rest are commented out.
var activeTemplate = map[string]bool{
	"schema_version": true,
	"listeners":      true, "listeners[]": true, "listeners[].name": true, "listeners[].address": true,
	"listeners[].protocols": true,
	"routes":                true, "routes[]": true, "routes[].name": true, "routes[].handler": true,
	"routes[].handler.module": true, "routes[].handler.params": true, "respond.body": true,
	"upstreams": true, "upstreams[]": true, "upstreams[].name": true, "upstreams[].addresses": true,
}

// examples are values for fields that have no default, by path, or by
// name where any field of the name takes it.
var templateExamples = map[string]string{
	"schema_version":   `1`,
	"listeners[].name": `"web"`, "listeners[].address": `":8080"`, "listeners[].protocols": `["http/1.1", "h2c"]`,
	"cert_file": `"/etc/fit/example.com.pem"`, "key_file": `"/etc/fit/example.com.key"`,
	"ca_files": `["/etc/fit/clients-ca.pem"]`, "ca_file": `"/etc/fit/ca.pem"`,
	"listeners[].tls.acme": `["example.com", "www.example.com"]`,
	"clients":              `["10.0.0.0/8"]`, "allow": `["*.example.com", "10.1.0.0/16"]`, "deny": `["169.254.169.254"]`,
	"ports": `[443]`, "trusted": `["10.0.0.0/8"]`, "trusted_proxies": `["10.0.0.0/8"]`,
	"routes[].name": `"hello"`, "routes[].listeners": `["web"]`, "routes[].priority": `10`,
	"routes[].match.host": `["example.com", "*.example.com"]`, "routes[].match.path": `["/api/*"]`,
	"routes[].match.path_regexp": `["^/v[0-9]+/"]`, "routes[].match.method": `["GET", "POST"]`,
	"routes[].match.header": `{"X-Env": ["prod"]}`, "routes[].match.query": `{"debug": []}`,
	"routes[].match.remote_ip": `["10.0.0.0/8"]`,
	"routes[].match.expr":      `"Host(\"example.com\") && (PathPrefix(\"/api/\") || Method(\"POST\"))"`,
	"upstreams[].name":         `"app"`, "upstreams[].addresses": `["127.0.0.1:9000", "127.0.0.1:9001"]`,
	"upstreams[].weights": `[3, 1]`, "upstreams[].hash_key": `"X-User"`,
	"upstreams[].tls_server_name": `"app.internal"`, "tls_cert_file": `"/etc/fit/client.pem"`,
	"tls_key_file": `"/etc/fit/client.key"`, "tls_ca_file": `"/etc/fit/upstream-ca.pem"`,
	"max_age": `"24h"`, "upstreams[].health_check.host": `"app.internal"`,
	"upstreams[].health_check.port": `8081`, "expect_status": `[200, 204]`,
	"services[].name": `"postgres"`, "services[].listeners": `["postgres"]`, "services[].upstream": `"db"`,
	"server_names": `["db.example.com"]`, "services[].idle_timeout": `"30m"`,
	"access_log.output": `"/var/log/fit/access.log"`,
	"headers_file":      `"/etc/fit/otlp-headers"`, "service_name": `"fit"`, "sample_ratio": `0.1`,
	"token_file": `"/etc/fit/token"`, "email": `"ops@example.com"`, "storage": `"/var/lib/fit"`,
	"acme.on_demand.ask": `"http://127.0.0.1:5555/allowed"`,
	"addresses":          `["127.0.0.1:6379"]`, "endpoints": `["http://127.0.0.1:2379"]`, "endpoint": `"http://127.0.0.1:8500"`,
	"username": `"fit"`, "password_file": `"/etc/fit/password"`, "datacenter": `"dc1"`, "key_prefix": `"fit/"`,
	"network": `"web"`, "kubeconfig": `"/etc/fit/kubeconfig"`, "context": `"prod"`,
	"namespaces": `["default"]`, "listen_host": `"0.0.0.0"`, "certificates_dir": `"/var/lib/fit/k8s-certs"`,
	"providers[].name": `"docker"`, "providers[].kubernetes.addresses": `["203.0.113.10"]`,
	"realm": `"fit"`, "user_header": `"X-User"`, "username_user": `"alice"`,
	"password_hash":  `"$2a$14$Zkx19XLiW6VYouLHR5NmfOFU0z2GTNmpkT/5qqR7hx4IjWJPDhjvG"`,
	"subject_header": `"X-Client-Subject"`, "max": `100`, "max_bytes": `10485760`,
	"upstream": `"app"`, "uri": `"/verify"`, "request_headers": `["Cookie", "Authorization"]`,
	"copy_headers": `["Remote-User", "Remote-Groups"]`, "set": `{"X-Env": "prod"}`,
	"add": `{"Cache-Control": ["no-store"]}`, "delete": `["Server", "X-Debug-*"]`,
	"jwks_url": `"https://auth.example.com/.well-known/jwks.json"`, "public_key_files": `["/etc/fit/jwt.pem"]`,
	"hmac_secret_file": `"/etc/fit/jwt-secret"`, "issuer": `"https://auth.example.com/"`, "audience": `["api"]`,
	"leeway": `"30s"`, "claims_headers": `{"X-User": "sub"}`, "rate": `10`, "burst": `20`,
	"to": `"https://{host}{uri}"`, "regexp": `"^/old/(.*)$"`, "replacement": `"/new/$1"`,
	"strip_prefix": `["/api"]`, "add_prefix": `"/v2"`, "path": `"/etc/fit/plugins/filter.wasm"`,
	"config": `"{}"`, "vm_config": `"{}"`, "value": `"..."`,
	"address": `"127.0.0.1:9000"`, "root": `"/srv/www"`, "env": `{"APP_ENV": "prod"}`,
	"body": `"hello from fit\n"`, "respond.body": `"hello from fit\n"`, "respond.headers": `{"Cache-Control": "no-store"}`,
	"failover": `"app-backup"`, "host_header": `"app.internal"`, "timeout": `"30s"`,
	"default_ttl": `"10m"`, "stale_while_revalidate": `"1m"`, "stale_if_error": `"1h"`,
	"slow_threshold": `"2s"`, "statuses": `[502, 503, 504]`,
	"cache.disk.path": `"/var/cache/fit"`, "wasm.properties[].path": `["plugin_name"]`,
	"proxy.upstreams[].upstream": `"app-canary"`, "proxy.upstreams[].weight": `5`,
	"proxy.mirror.upstream": `"shadow"`, "split_path": `[".php"]`,
	"static.try_files":  `["{path}", "{path}/", "/index.html"]`,
	"fastcgi.try_files": `["{path}", "{path}/index.php", "/index.php"]`,
}

// object makes the members of struct type t, at path; mod is the module
// whose params t is, for the examples' paths.
func (t *templater) object(typ reflect.Type, path string, mod *module.Info) *tnode {
	n := &tnode{isObject: true}
	for _, f := range config.Fields(typ) {
		if f.Self {
			continue
		}
		n.children = append(n.children, t.field(f, join(path, f.Name), mod))
		for _, old := range f.Renamed {
			n.children = append(n.children, &tnode{name: old, scalar: "null",
				doc: []string{fmt.Sprintf("Deprecated: renamed %s; the old name goes in config version 2.", f.Name)}})
		}
	}
	return n
}

// examplePath is a field's path for the examples: a module's params' own
// paths start with the module's name.
func examplePath(path string, mod *module.Info) string {
	if mod != nil {
		return mod.Name() + "." + path
	}
	return path
}

func (t *templater) field(f config.Field, path string, mod *module.Info) *tnode {
	ep := examplePath(path, mod)
	n := &tnode{name: f.Name, active: activeTemplate[ep] || mod == nil && activeTemplate[path]}
	n.doc = fieldDoc(f, t.typeName(f.Type, &f))
	typ := f.Type
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	switch {
	case typ == moduleRefType:
		return t.moduleField(n, f, path)
	case typ == valueType:
		n.scalar = "{}"
	case typ.Kind() == reflect.Struct && typ != config.DurationType:
		o := t.object(typ, path, mod)
		n.children, n.isObject = o.children, true
	case typ.Kind() == reflect.Slice && elemStruct(typ) && typ.Elem() != moduleRefType:
		n.isArray = true
		o := t.object(deref(typ.Elem()), path+"[]", mod)
		o.active = activeTemplate[examplePath(path+"[]", mod)]
		n.elems = []*tnode{o}
		if path == "routes" && mod == nil {
			n.elems = append(n.elems, t.handlerRoutes()...)
		}
	case typ.Kind() == reflect.Slice && typ.Elem() == moduleRefType:
		n.isArray = true
		n.elems = t.middleware()
	default:
		n.scalar = scalarExample(f, ep, typ)
	}
	return n
}

func deref(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

func elemStruct(t reflect.Type) bool {
	e := deref(t.Elem())
	return e.Kind() == reflect.Struct && e != config.DurationType && e != valueType
}

// moduleField makes a route's handler: respond, written out; the other
// handlers have routes of their own (see handlerRoutes).
func (t *templater) moduleField(n *tnode, f config.Field, path string) *tnode {
	n.isObject = true
	n.children = t.moduleRef(module.Handlers, "respond", path)
	return n
}

// moduleRef makes the members of a module reference: its name and its
// params, every field of them.
func (t *templater) moduleRef(ns, name, _ string) []*tnode {
	info, ok := module.Lookup(ns + "." + name)
	if !ok {
		return nil
	}
	params := t.object(reflect.TypeOf(info.New()).Elem(), "", &info)
	return []*tnode{
		{name: "module", scalar: strconv.Quote(name), active: true, doc: wrap(info.Doc)},
		{name: "params", isObject: true, children: params.children, active: true},
	}
}

// handlerRoutes makes a route for each handler but respond, which the
// first route has: its name and the handler, with all its params,
// commented out.
func (t *templater) handlerRoutes() []*tnode {
	var out []*tnode
	for _, info := range module.Namespace(module.Handlers) {
		if info.Name() == "respond" {
			continue
		}
		out = append(out, &tnode{isObject: true, children: []*tnode{
			{name: "name", scalar: strconv.Quote(info.Name() + "-example"), active: true,
				doc: []string{"A route whose handler is " + info.Name() + ", with every one of its params."}},
			{name: "handler", isObject: true, active: true, children: t.moduleRef(module.Handlers, info.Name(), "")},
		}})
	}
	return out
}

// middleware makes a route's middleware: every middleware module, each
// with all its params, commented out.
func (t *templater) middleware() []*tnode {
	var out []*tnode
	for _, info := range module.Namespace(module.Middlewares) {
		out = append(out, &tnode{isObject: true, children: t.moduleRef(module.Middlewares, info.Name(), "")})
	}
	return out
}

// typeName says what type a field is, for its comment.
func (t *templater) typeName(typ reflect.Type, f *config.Field) string {
	switch {
	case typ == valueType:
		return "any"
	case typ == config.DurationType:
		return `duration, as "1.5s" or "2m"`
	case typ == moduleRefType:
		return "module"
	}
	switch typ.Kind() {
	case reflect.Pointer:
		return t.typeName(typ.Elem(), f)
	case reflect.Struct:
		return "object"
	case reflect.Slice:
		if f != nil && len(f.Enum) > 0 {
			return "list of: " + strings.Join(f.Enum, ", ")
		}
		return "list of " + t.typeName(typ.Elem(), nil)
	case reflect.Map:
		return "object of " + t.typeName(typ.Elem(), nil)
	case reflect.String:
		if f != nil && len(f.Enum) > 0 {
			return "one of: " + strings.Join(f.Enum, ", ")
		}
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return withBounds("integer", f, typ.Kind() >= reflect.Uint)
	case reflect.Float32, reflect.Float64:
		return withBounds("number", f, false)
	}
	return typ.String()
}

// fieldDoc is a field's comment: its description, then its type, default
// and whether it is required.
func fieldDoc(f config.Field, typ string) []string {
	lines := wrap(f.Doc)
	meta := typ
	if f.Default != "" {
		meta += "; default " + f.Default
	}
	if f.Required {
		meta += "; required"
	}
	return append(lines, meta+".")
}

// wrap wraps text at 92 columns.
func wrap(text string) []string {
	var lines []string
	line := ""
	for w := range strings.FieldsSeq(text) {
		if line != "" && len(line)+1+len(w) > 92 {
			lines = append(lines, line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += w
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}

// scalarExample is a scalar field's value in the template: an example of
// its own, its default, or one its type and bounds allow.
func scalarExample(f config.Field, path string, typ reflect.Type) string {
	if v, ok := templateExamples[path]; ok {
		return v
	}
	if f.Default != "" {
		return f.Default
	}
	if v, ok := templateExamples[f.Name]; ok {
		return v
	}
	if typ == config.DurationType {
		return `"10s"`
	}
	switch typ.Kind() {
	case reflect.String:
		if typ == config.DurationType {
			return `"10s"`
		}
		if len(f.Enum) > 0 {
			return strconv.Quote(f.Enum[0])
		}
		return `""`
	case reflect.Bool:
		return "true"
	case reflect.Slice:
		if len(f.Enum) > 0 {
			b, _ := json.Marshal(f.Enum)
			return string(b)
		}
		return "[]"
	case reflect.Map:
		return "{}"
	case reflect.Float32, reflect.Float64, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if f.Min != nil {
			return strconv.FormatFloat(*f.Min, 'f', -1, 64)
		}
		return "0"
	}
	if typ == config.DurationType {
		return `"10s"`
	}
	return "null"
}

// tmplWriter writes the template's nodes as JSON with comments, YAML, or
// a .conf file.
type tmplWriter struct {
	buf  *bytes.Buffer
	yaml bool
	fit  bool
}

func (w *tmplWriter) comment() string {
	if w.yaml || w.fit {
		return "#"
	}
	return "//"
}

func (w *tmplWriter) head() {
	c := w.comment()
	for _, l := range []string{
		"fit's config template: every field of the config and of every module's params.",
		"Generated by schema.Template; regenerate with: go test ./schema -update",
		"",
		"What is written out is a small config fit runs as it is: a listener on :8080, an",
		"upstream, and a route answering \"hello from fit\". Every other field is commented",
		"out, with its default, or an example where it has none: uncomment what you need.",
		"Above each field: what it is, its type, its choices and its default. A field left out",
		"takes its default. The config reference, docs/reference/config.md, has the same, with",
		"examples; the templates in the other formats are beside this one's directory.",
	} {
		if l == "" {
			fmt.Fprintf(w.buf, "%s\n", c)
		} else {
			fmt.Fprintf(w.buf, "%s %s\n", c, l)
		}
	}
	if !w.yaml && !w.fit {
		w.buf.WriteString("{\n")
	}
}

// line writes a line at indent, commented out unless on.
func (w *tmplWriter) line(indent int, on bool, text string) {
	pad := strings.Repeat("  ", indent)
	if w.fit {
		pad = strings.Repeat("\t", indent)
	}
	if on {
		fmt.Fprintf(w.buf, "%s%s\n", pad, text)
	} else {
		fmt.Fprintf(w.buf, "%s%s %s\n", pad, w.comment(), text)
	}
}

// docs writes a member's comment; inside a commented-out member, as a
// comment within the comment, so that uncommenting it leaves a comment.
func (w *tmplWriter) docs(indent int, on bool, doc []string) {
	for _, d := range doc {
		w.line(indent, on, w.comment()+" "+d)
	}
}

// members writes the config's members at indent.
func (w *tmplWriter) members(ms []*tnode, indent int) {
	for i, m := range ms {
		if i > 0 {
			w.buf.WriteString("\n")
		}
		w.member(m, indent, true)
	}
}

// member writes m; parentOn says the object it is in is written out.
func (w *tmplWriter) member(m *tnode, indent int, parentOn bool) {
	on := parentOn && m.active
	w.docs(indent, parentOn, m.doc)
	key := m.name
	if !w.yaml {
		key = strconv.Quote(m.name)
	}
	switch {
	case m.isObject:
		if w.yaml {
			w.line(indent, on, key+":")
		} else {
			w.line(indent, on, key+": {")
		}
		for i, c := range m.children {
			if i > 0 && len(c.doc) > 0 {
				w.buf.WriteString("\n")
			}
			w.member(c, indent+1, on)
		}
		if !w.yaml {
			w.line(indent, on, "},")
		}
	case m.isArray:
		if w.yaml {
			w.line(indent, on, key+":")
		} else {
			w.line(indent, on, key+": [")
		}
		for _, e := range m.elems {
			w.element(e, indent+1, on)
		}
		if !w.yaml {
			w.line(indent, on, "],")
		}
	default:
		if w.yaml {
			w.line(indent, on, key+": "+m.scalar)
		} else {
			w.line(indent, on, key+": "+m.scalar+",")
		}
	}
}

// element writes an array's element, an object.
func (w *tmplWriter) element(e *tnode, indent int, parentOn bool) {
	on := parentOn && e.active
	if w.yaml {
		w.line(indent, on, "-")
	} else {
		w.line(indent, on, "{")
	}
	for i, c := range e.children {
		if i > 0 && len(c.doc) > 0 {
			w.buf.WriteString("\n")
		}
		w.member(c, indent+1, on)
	}
	if !w.yaml {
		w.line(indent, on, "},")
	}
}

// fitMembers writes members as directives of a .conf file; root says they
// are the config's own, whose lists of objects take their names in the
// singular.
func (w *tmplWriter) fitMembers(ms []*tnode, indent int, on, root bool) {
	for i, m := range ms {
		if m.scalar == "null" {
			continue // an old name, which a .conf file need not show
		}
		if i > 0 && (root || len(m.doc) > 0) {
			w.buf.WriteString("\n")
		}
		w.fitMember(m, indent, on, root)
	}
}

// fitMember writes m; parentOn says the block it is in is written out.
func (w *tmplWriter) fitMember(m *tnode, indent int, parentOn, root bool) {
	on := parentOn && m.active
	w.docs(indent, parentOn, m.doc)
	name := m.name
	switch {
	case m.isObject && len(m.children) == 2 && m.children[0].name == "module":
		w.fitModule(name, m.children, indent, on)
	case m.isObject:
		w.line(indent, on, name+" {")
		w.fitMembers(m.children, indent+1, on, false)
		w.line(indent, on, "}")
	case m.isArray:
		if root {
			if s, ok := fitSingular[name]; ok {
				name = s
			}
		}
		for i, e := range m.elems {
			if i > 0 {
				w.buf.WriteString("\n")
			}
			eon := on && e.active
			if len(e.children) == 2 && e.children[0].name == "module" {
				w.fitModule(name, e.children, indent, eon)
				continue
			}
			w.line(indent, eon, name+" {")
			w.fitMembers(e.children, indent+1, eon, false)
			w.line(indent, eon, "}")
		}
	default:
		for _, l := range fitScalar(name, m.scalar) {
			w.line(indent, on, l)
		}
	}
}

// fitModule writes a module reference: name, the module's, and its params.
func (w *tmplWriter) fitModule(name string, ref []*tnode, indent int, on bool) {
	mod, _ := strconv.Unquote(ref[0].scalar)
	w.docs(indent, on, ref[0].doc)
	w.line(indent, on, name+" "+mod+" {")
	w.fitMembers(ref[1].children, indent+1, on, false)
	w.line(indent, on, "}")
}

// fitSingular are the names of the config's lists of objects in a .conf
// file, as fitconf takes them.
var fitSingular = map[string]string{
	"listeners": "listener", "routes": "route", "upstreams": "upstream", "services": "service", "providers": "provider",
}

// fitScalar writes a field whose value is v, as JSON, as the lines of a
// .conf file: a scalar or a list as arguments, an object as a block of its
// keys.
func fitScalar(name, v string) []string {
	var x any
	dec := json.NewDecoder(strings.NewReader(v))
	dec.UseNumber()
	if err := dec.Decode(&x); err != nil {
		return []string{name + " " + v}
	}
	switch x := x.(type) {
	case map[string]any:
		if len(x) == 0 {
			return []string{name + " {}"}
		}
		lines := []string{name + " {"}
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		slices.Sort(keys)
		for _, k := range keys {
			lines = append(lines, "\t"+strings.Join(append([]string{fitconf.Word(k)}, fitWords(x[k])...), " "))
		}
		return append(lines, "}")
	}
	return []string{strings.Join(append([]string{name}, fitWords(x)...), " ")}
}

func fitWords(v any) []string {
	switch v := v.(type) {
	case []any:
		var out []string
		for _, e := range v {
			out = append(out, fitWords(e)...)
		}
		return out
	case string:
		return []string{fitconf.Word(v)}
	case json.Number:
		return []string{string(v)}
	case bool:
		return []string{strconv.FormatBool(v)}
	}
	return nil
}
