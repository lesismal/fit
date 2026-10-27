package main

import "strings"

// A server under test: how its container is run, and what it can serve.
type server struct {
	name string
	// image is the default image, overridden by the -<name>-image flag.
	image string
	// scenarios are the scenarios the server can serve, and idle the kinds
	// of idle connection it can hold.
	scenarios []string
	idle      []string
	// mounts maps a file under the rendered configs, "www" for the static
	// files or "certs" for the TLS certificate, to where the container
	// expects it.
	mounts [][2]string
	// cmd, when set, replaces the image's command.
	cmd []string
	// version prints the server's version inside its container.
	version []string
	// ready answers 200 once the server has loaded its config.
	ready string
	// fit marks a server run from the fit binary built for the harness.
	fit bool
	// admin is the address of the server's Admin API, whose /metrics has
	// go_goroutines, for counting its goroutines; "" when it has none.
	admin string
}

// scenario is what the load generators ask a server for.
type scenario struct {
	name, path, body string
	// protocols, when set, are the only ones the scenario runs over.
	protocols []string
	// script is wrk's script, report.lua when empty.
	script string
	// backendRoute, when set, is the cache backend's route whose requests
	// are counted during the run, for the hit ratio.
	backendRoute string
	// kind is how the load generators ask, as workloads.go has it: the one
	// path again and again when empty.
	kind string
	// arg is the kind's argument: the path prefix of kindSite, the requests
	// a connection carries for kindChurn, the body's size for kindUpload.
	arg string
}

// nginxImage is nginx's, the server's and the backends'.
const nginxImage = "nginx:1.31.4"

// cacheBody is what the cache origin answers with: 4 KiB.
var cacheBody = strings.Repeat("0123456789abcdef", 256)

// The scenarios: a fixed response, a 1 KiB static file, and a proxy to a
// backend that answers with the same fixed response; and a cache in front of
// a backend that answers every path with 4 KiB fresh for 10 minutes, asked
// for one path again and again, and for 100,000 paths, the more popular ones
// the more often, by Zipf's law.
//
// Then the workloads of the plan in bench/README.md, which model what a
// proxy is used for (workloads.go has their payloads and headers): an API
// gateway's mix of small GETs and POSTs; a website's files, sized as pages
// and assets are, by popularity, served and behind a cache; connections that
// carry one request, or ten, each with its TLS handshake; and 1 MiB
// downloads and uploads.
var scenarios = []scenario{
	{name: "fixed", path: "/fixed", body: "hello, world"},
	{name: "static", path: "/static/small.html"}, // the body is www/static/small.html
	{name: "proxy", path: "/proxy", body: "hello, world"},
	{name: "cache-hot", path: "/cache/hot", body: cacheBody, backendRoute: "cache"},
	{name: "cache-zipf", path: "/cache/1", body: cacheBody, protocols: []string{"http/1.1"}, script: "zipf.lua", backendRoute: "cache"},
	{name: "api", path: "/api/item", body: apiBody, kind: kindAPI},
	{name: "site", kind: kindSite, arg: "/site/", protocols: httpsProtocols},
	{name: "edge", kind: kindSite, arg: "/edge/", protocols: httpsProtocols, backendRoute: "edge"},
	{name: "churn-1", path: "/fixed", body: "hello, world", kind: kindChurn, arg: "1", protocols: []string{"https/1.1"}},
	{name: "churn-10", path: "/fixed", body: "hello, world", kind: kindChurn, arg: "10", protocols: []string{"https/1.1"}},
	{name: "download", path: "/site/" + largeFile},
	{name: "proxy-download", path: "/proxy/large", body: largeBody},
	{name: "proxy-upload", path: "/proxy/upload", body: apiPostReply, kind: kindUpload, arg: largeSize},
}

// httpsProtocols are the protocols of the website workload: a browser's,
// all of them over TLS.
var httpsProtocols = []string{"https/1.1", "h2", "h3"}

// The protocols, each on its own port in every server's config, and the
// load generator that drives it: https/1.1 is HTTP/1.1 over TLS, h2 HTTP/2
// over TLS, and HTTP/3's on UDP, over TLS, by h2load built with HTTP/3 in an
// image of its own.
type protocol struct{ name, port, tool string }

var protocols = []protocol{
	{"http/1.1", "8080", "wrk"},
	{"https/1.1", "8443", "wrk"},
	{"h2c", "8081", "h2load"},
	{"h2", "8445", "h2load"},
	{"h3", "8444", "h2load-h3"},
}

var servers = []server{
	{
		name:  "nginx",
		image: nginxImage,
		scenarios: []string{"fixed", "static", "proxy", "cache-hot", "cache-zipf", "api", "site", "edge", "churn-1", "churn-10",
			"download", "proxy-download", "proxy-upload"},
		idle: []string{"http1", "tls", "ws"},
		mounts: [][2]string{{"nginx/nginx.conf", "/etc/nginx/nginx.conf"}, {"nginx/routes.conf", "/etc/nginx/routes.conf"},
			{"www", "/srv/www"}, {"certs", "/certs"}},
		version: []string{"nginx", "-v"},
		ready:   "/fixed",
	},
	{
		name:      "caddy",
		image:     "caddy:2.10.2",
		scenarios: []string{"fixed", "static", "proxy", "api", "site", "churn-1", "churn-10", "download", "proxy-download", "proxy-upload"},
		idle:      []string{"http1", "tls", "ws"},
		mounts:    [][2]string{{"caddy/Caddyfile", "/etc/caddy/Caddyfile"}, {"www", "/srv/www"}, {"certs", "/certs"}},
		version:   []string{"caddy", "version"},
		ready:     "/fixed",
	},
	{
		name:      "traefik",
		image:     "traefik:v3.5.0",
		scenarios: []string{"proxy", "api", "proxy-download", "proxy-upload"},
		idle:      []string{"http1", "tls", "ws"},
		mounts: [][2]string{{"traefik/traefik.yml", "/etc/traefik/traefik.yml"}, {"traefik/dynamic.yml", "/etc/traefik/dynamic.yml"},
			{"certs", "/certs"}},
		version: []string{"traefik", "version"},
		// Traefik listens before its file provider has loaded the routes.
		ready: "/proxy",
	},
	{
		name:      "envoy",
		image:     "envoyproxy/envoy:v1.40.0",
		scenarios: []string{"fixed", "proxy", "api", "churn-1", "churn-10", "proxy-download", "proxy-upload"},
		idle:      []string{"http1", "tls", "ws"},
		mounts:    [][2]string{{"envoy/envoy.yaml", "/etc/envoy/envoy.yaml"}, {"certs", "/certs"}},
		// One worker per CPU the container is pinned to, not per CPU of the
		// host.
		cmd:     []string{"envoy", "-c", "/etc/envoy/envoy.yaml", "--cpuset-threads", "--log-level", "error"},
		version: []string{"envoy", "--version"},
		ready:   "/fixed",
	},
	{
		name: "fit",
		scenarios: []string{"fixed", "static", "proxy", "cache-hot", "cache-zipf", "api", "site", "edge", "churn-1", "churn-10",
			"download", "proxy-download", "proxy-upload"},
		idle:    []string{"http1", "tls", "ws"},
		mounts:  [][2]string{{"fit/fit.conf", "/etc/fit/fit.conf"}, {"www", "/srv/www"}, {"certs", "/certs"}},
		cmd:     []string{"/usr/local/bin/fit", "run", "-c", "/etc/fit/fit.conf"},
		version: []string{"/usr/local/bin/fit", "version"},
		ready:   "/fixed",
		fit:     true,
		admin:   "127.0.0.1:9005",
	},
}

// backend is what the proxy scenarios proxy to, and what Traefik's
// readiness is checked through: nginx answering every request with the fixed
// response on port 9000, but the API workload's, the large download's and
// the upload's paths (configs/backends/proxy.conf). The backends are nginx,
// fast on their 2 CPUs and none of the servers' own, the same for every
// server under test.
var backend = server{
	name:   "backend",
	image:  nginxImage,
	mounts: [][2]string{{"backends/proxy.conf", "/etc/nginx/nginx.conf"}, {"www", "/srv/www"}},
	ready:  "/",
}

// cacheBackend is what the cache scenarios' caches fetch from, the origin:
// nginx answering every /cache/ path with cacheBody, and /edge/ paths with
// the website's files, on port 9003, counting what it answers with
// stub_status at 127.0.0.1:9004, from which the hit ratio is worked out
// (configs/backends/origin.conf). It is a process of its own, so that only
// the caches' requests are counted.
var cacheBackend = server{
	name:   "cachebackend",
	image:  nginxImage,
	mounts: [][2]string{{"backends/origin.conf", "/etc/nginx/nginx.conf"}, {"www", "/srv/www"}},
	ready:  "/cache/ready",
}
