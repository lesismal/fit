package main

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
}

// The scenarios: a fixed response, a 1 KiB static file, and a proxy to a
// backend that answers with the same fixed response.
var scenarios = []struct{ name, path, body string }{
	{"fixed", "/fixed", "hello, world"},
	{"static", "/static/small.html", ""}, // the body is www/static/small.html
	{"proxy", "/proxy", "hello, world"},
}

// The protocols, each on its own port in every server's config, and the
// load generator that drives it.
var protocols = []struct{ name, port, tool string }{
	{"http/1.1", "8080", "wrk"},
	{"h2c", "8081", "h2load"},
}

var servers = []server{
	{
		name:      "nginx",
		image:     "nginx:1.31.4",
		scenarios: []string{"fixed", "static", "proxy"},
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
		name:      "fit",
		scenarios: []string{"fixed", "static", "proxy"},
		idle:      []string{"http1", "tls", "ws"},
		mounts:    [][2]string{{"fit/fit.json", "/etc/fit/fit.json"}, {"www", "/srv/www"}, {"certs", "/certs"}},
		cmd:       []string{"/usr/local/bin/fit", "run", "-c", "/etc/fit/fit.json"},
		version:   []string{"/usr/local/bin/fit", "version"},
		ready:     "/fixed",
		fit:       true,
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
