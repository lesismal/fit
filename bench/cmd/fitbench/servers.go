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
	// hitRatio says to count the requests that reach the cache backend.
	hitRatio bool
}

// cacheBody is what the cache backend answers with: 4 KiB.
var cacheBody = strings.Repeat("0123456789abcdef", 256)

// The scenarios: a fixed response, a 1 KiB static file, and a proxy to a
// backend that answers with the same fixed response; and a cache in front of
// a backend that answers every path with 4 KiB fresh for 10 minutes, asked
// for one path again and again, and for 100,000 paths, the more popular ones
// the more often, by Zipf's law.
var scenarios = []scenario{
	{name: "fixed", path: "/fixed", body: "hello, world"},
	{name: "static", path: "/static/small.html"}, // the body is www/static/small.html
	{name: "proxy", path: "/proxy", body: "hello, world"},
	{name: "cache-hot", path: "/cache/hot", body: cacheBody, hitRatio: true},
	{name: "cache-zipf", path: "/cache/1", body: cacheBody, protocols: []string{"http/1.1"}, script: "zipf.lua", hitRatio: true},
}

// The protocols, each on its own port in every server's config, and the
// load generator that drives it: h2 is HTTP/2 over TLS, and HTTP/3's on
// UDP, over TLS, by h2load built with HTTP/3 in an image of its own.
var protocols = []struct{ name, port, tool string }{
	{"http/1.1", "8080", "wrk"},
	{"h2c", "8081", "h2load"},
	{"h2", "8445", "h2load"},
	{"h3", "8444", "h2load-h3"},
}

var servers = []server{
	{
		name:      "nginx",
		image:     "nginx:1.31.4",
		scenarios: []string{"fixed", "static", "proxy", "cache-hot", "cache-zipf"},
		idle:      []string{"http1", "tls", "ws"},
		mounts:    [][2]string{{"nginx/nginx.conf", "/etc/nginx/nginx.conf"}, {"www", "/srv/www"}, {"certs", "/certs"}},
		version:   []string{"nginx", "-v"},
		ready:     "/fixed",
	},
	{
		name:      "caddy",
		image:     "caddy:2.10.2",
		scenarios: []string{"fixed", "static", "proxy"},
		idle:      []string{"http1", "tls", "ws"},
		mounts:    [][2]string{{"caddy/Caddyfile", "/etc/caddy/Caddyfile"}, {"www", "/srv/www"}, {"certs", "/certs"}},
		version:   []string{"caddy", "version"},
		ready:     "/fixed",
	},
	{
		name:      "traefik",
		image:     "traefik:v3.5.0",
		scenarios: []string{"proxy"},
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
		scenarios: []string{"fixed", "proxy"},
		idle:      []string{"http1", "tls", "ws"},
		mounts:    [][2]string{{"envoy/envoy.yaml", "/etc/envoy/envoy.yaml"}, {"certs", "/certs"}},
		// One worker per CPU the container is pinned to, not per CPU of the
		// host.
		cmd:     []string{"envoy", "-c", "/etc/envoy/envoy.yaml", "--cpuset-threads", "--log-level", "error"},
		version: []string{"envoy", "--version"},
		ready:   "/fixed",
	},
	{
		name:      "fit",
		scenarios: []string{"fixed", "static", "proxy", "cache-hot", "cache-zipf"},
		idle:      []string{"http1", "tls", "ws"},
		mounts:    [][2]string{{"fit/fit.json", "/etc/fit/fit.json"}, {"www", "/srv/www"}, {"certs", "/certs"}},
		cmd:       []string{"/usr/local/bin/fit", "run", "-c", "/etc/fit/fit.json"},
		version:   []string{"/usr/local/bin/fit", "version"},
		ready:     "/fixed",
		fit:       true,
		admin:     "127.0.0.1:9005",
	},
}

// backend is what the proxy scenario proxies to, and what Traefik's
// readiness is checked through: fit answering every request with the fixed
// response, on port 9000.
var backend = server{
	name:   "backend",
	mounts: [][2]string{{"fit/backend.json", "/etc/fit/fit.json"}},
	cmd:    []string{"/usr/local/bin/fit", "run", "-c", "/etc/fit/fit.json"},
	ready:  "/",
	fit:    true,
}

// cacheBackend is what the cache scenarios' caches fetch from: fit answering
// every path with cacheBody on port 9003, counting what it answers on its
// Admin API at 127.0.0.1:9004, from which the hit ratio is worked out. It is
// a backend of its own, so that the counting costs the proxy scenario's
// backend nothing.
var cacheBackend = server{
	name:   "cachebackend",
	mounts: [][2]string{{"fit/cache-backend.json", "/etc/fit/fit.json"}},
	cmd:    []string{"/usr/local/bin/fit", "run", "-c", "/etc/fit/fit.json"},
	ready:  "/cache/ready",
	fit:    true,
}
