package fastcgi

import (
	"net"
	"path"
	"path/filepath"
	"strconv"
	"strings"
)

// params encodes the request's CGI/1.1 parameters, as PHP and other
// FastCGI applications read them: what the request is, the script it runs
// and its path info under the root, who sent it and to where, and each of
// its fields as HTTP_ and its name, but Proxy (httpoxy) and the ones CGI
// has names of its own for; then the config's env.
func (x *exchange) params() []byte {
	r, h := x.c.Request, x.h
	scheme, port := "http", "80"
	if r.TLS != nil {
		scheme, port = "https", "443"
	}
	host := r.Host
	if hh, p, err := net.SplitHostPort(r.Host); err == nil {
		host, port = hh, p
	}
	remoteAddr, remotePort, _ := net.SplitHostPort(r.RemoteAddr)
	serverAddr := ""
	if x.c.Conn != nil {
		if a, ok := x.c.Conn.LocalAddr().(*net.TCPAddr); ok {
			serverAddr, port = a.IP.String(), strconv.Itoa(a.Port)
		}
	}
	script := path.Clean(x.script)
	filename := filepath.Join(h.Root, filepath.FromSlash(script))
	requestURI := r.RequestURI
	if requestURI == "" {
		requestURI = r.URL.RequestURI()
	}
	contentLength := ""
	if r.ContentLength >= 0 && (r.ContentLength > 0 || r.Method != "GET" && r.Method != "HEAD") {
		contentLength = strconv.FormatInt(r.ContentLength, 10)
	}
	var b []byte
	add := func(name, value string) { b = appendParam(b, name, value) }
	add("GATEWAY_INTERFACE", "CGI/1.1")
	add("SERVER_SOFTWARE", "fit")
	add("SERVER_PROTOCOL", r.Proto)
	add("SERVER_NAME", host)
	add("SERVER_ADDR", serverAddr)
	add("SERVER_PORT", port)
	add("REQUEST_SCHEME", scheme)
	if scheme == "https" {
		add("HTTPS", "on")
	}
	add("REMOTE_ADDR", remoteAddr)
	add("REMOTE_PORT", remotePort)
	add("REQUEST_METHOD", r.Method)
	add("REQUEST_URI", requestURI)
	add("QUERY_STRING", r.URL.RawQuery)
	add("DOCUMENT_ROOT", h.Root)
	add("DOCUMENT_URI", r.URL.Path)
	add("SCRIPT_NAME", script)
	add("SCRIPT_FILENAME", filename)
	add("PATH_INFO", x.pathInfo)
	if x.pathInfo != "" {
		add("PATH_TRANSLATED", filepath.Join(h.Root, filepath.FromSlash(x.pathInfo)))
	}
	add("CONTENT_TYPE", r.Header.Get("Content-Type"))
	add("CONTENT_LENGTH", contentLength)
	// PHP built with force-cgi-redirect runs only for a request a server
	// passed on.
	add("REDIRECT_STATUS", "200")
	for name, values := range r.Header {
		switch name {
		case "Proxy", "Content-Type", "Content-Length":
			continue
		}
		key := "HTTP_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
		add(key, strings.Join(values, ", "))
	}
	if r.Host != "" {
		add("HTTP_HOST", r.Host)
	}
	for name, value := range h.Env {
		add(name, value)
	}
	return b
}
