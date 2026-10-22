package kubernetes

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/lesismal/fit/config"
)

// Bounds on what is read from the API server.
const (
	maxPageBytes  = 64 << 20
	maxErrorBytes = 4 << 10
	pageSize      = 500
	// watchSeconds is how long a watch lasts before the server ends it
	// and the provider makes another.
	watchSeconds = 300
)

// The service account's files, inside a pod; variables for tests.
var (
	serviceAccountToken = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	serviceAccountCA    = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
)

var (
	errNotFound = errors.New("not found")
	errGone     = errors.New("the resource version is too old")
	errConflict = errors.New("the object changed")
)

// client calls the API server.
type client struct {
	server  string // the API server's URL, without a trailing '/'
	hc      *http.Client
	timeout time.Duration
	token   *token // nil when no token is sent
}

// token is a bearer token, given or read from a file, which is read again
// a minute after it was last read, since projected tokens rotate.
type token struct {
	static string
	file   string

	mu     sync.Mutex
	cached string
	read   time.Time
}

func (t *token) get() (string, error) {
	if t.file == "" {
		return t.static, nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.cached == "" || time.Since(t.read) > time.Minute {
		data, err := os.ReadFile(t.file)
		if err != nil {
			if t.cached != "" {
				return t.cached, nil // the last one, until the file is back
			}
			return "", err
		}
		t.cached, t.read = strings.TrimSpace(string(data)), time.Now()
	}
	return t.cached, nil
}

// restConfig is where the API server is and how to authenticate to it.
type restConfig struct {
	server string
	tls    *tls.Config
	token  *token
	// where says where the config came from, for messages.
	where string
}

// loadConfig finds how to reach the cluster: the kubeconfig opts names, or
// the pod's service account inside a cluster, or $KUBECONFIG, or
// ~/.kube/config.
func loadConfig(opts *config.KubernetesProvider) (*restConfig, error) {
	if opts.Kubeconfig != "" {
		return fromKubeconfig(opts.Kubeconfig, opts.Context)
	}
	if host := os.Getenv("KUBERNETES_SERVICE_HOST"); host != "" {
		return inCluster(host, os.Getenv("KUBERNETES_SERVICE_PORT"))
	}
	path := os.Getenv("KUBECONFIG")
	if path != "" {
		path, _, _ = strings.Cut(path, string(os.PathListSeparator))
	} else {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("no kubeconfig: not in a cluster, no $KUBECONFIG, and no home directory: %w", err)
		}
		path = filepath.Join(home, ".kube", "config")
	}
	return fromKubeconfig(path, opts.Context)
}

func inCluster(host, port string) (*restConfig, error) {
	ca, err := os.ReadFile(serviceAccountCA)
	if err != nil {
		return nil, fmt.Errorf("in the cluster: %w", err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("in the cluster: %s holds no certificate", serviceAccountCA)
	}
	if port == "" {
		port = "443"
	}
	t := &token{file: serviceAccountToken}
	if _, err := t.get(); err != nil {
		return nil, fmt.Errorf("in the cluster: %w", err)
	}
	return &restConfig{server: "https://" + net.JoinHostPort(host, port), tls: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12},
		token: t, where: "the pod's service account"}, nil
}

// kubeconfig is the part of a kubeconfig file the provider reads.
type kubeconfig struct {
	CurrentContext string `yaml:"current-context"`
	Clusters       []struct {
		Name    string `yaml:"name"`
		Cluster struct {
			Server        string `yaml:"server"`
			CA            string `yaml:"certificate-authority"`
			CAData        string `yaml:"certificate-authority-data"`
			Insecure      bool   `yaml:"insecure-skip-tls-verify"`
			TLSServerName string `yaml:"tls-server-name"`
		} `yaml:"cluster"`
	} `yaml:"clusters"`
	Users []struct {
		Name string `yaml:"name"`
		User struct {
			ClientCert     string     `yaml:"client-certificate"`
			ClientCertData string     `yaml:"client-certificate-data"`
			ClientKey      string     `yaml:"client-key"`
			ClientKeyData  string     `yaml:"client-key-data"`
			Token          string     `yaml:"token"`
			TokenFile      string     `yaml:"tokenFile"`
			Exec           *yaml.Node `yaml:"exec"`
			AuthProvider   *yaml.Node `yaml:"auth-provider"`
		} `yaml:"user"`
	} `yaml:"users"`
	Contexts []struct {
		Name    string `yaml:"name"`
		Context struct {
			Cluster string `yaml:"cluster"`
			User    string `yaml:"user"`
		} `yaml:"context"`
	} `yaml:"contexts"`
}

// fromKubeconfig reads the kubeconfig at path, and takes its context named
// context, or its current one.
func fromKubeconfig(path, context string) (*restConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("kubeconfig: %w", err)
	}
	var kc kubeconfig
	if err := yaml.Unmarshal(data, &kc); err != nil {
		return nil, fmt.Errorf("kubeconfig %s: %w", path, err)
	}
	dir := filepath.Dir(path)
	file := func(p string) string {
		if p != "" && !filepath.IsAbs(p) {
			return filepath.Join(dir, p)
		}
		return p
	}
	if context == "" {
		context = kc.CurrentContext
	}
	if context == "" {
		return nil, fmt.Errorf("kubeconfig %s: no current-context, and no context given", path)
	}
	var clusterName, userName string
	found := false
	for _, c := range kc.Contexts {
		if c.Name == context {
			clusterName, userName, found = c.Context.Cluster, c.Context.User, true
		}
	}
	if !found {
		return nil, fmt.Errorf("kubeconfig %s: no context %q", path, context)
	}
	rc := &restConfig{where: fmt.Sprintf("kubeconfig %s, context %s", path, context), tls: &tls.Config{MinVersion: tls.VersionTLS12}}
	found = false
	for _, c := range kc.Clusters {
		if c.Name != clusterName {
			continue
		}
		found = true
		cl := c.Cluster
		if cl.Server == "" {
			return nil, fmt.Errorf("kubeconfig %s: cluster %q has no server", path, clusterName)
		}
		rc.server = strings.TrimRight(cl.Server, "/")
		rc.tls.InsecureSkipVerify = cl.Insecure
		rc.tls.ServerName = cl.TLSServerName
		ca, err := pemOf(cl.CAData, file(cl.CA))
		if err != nil {
			return nil, fmt.Errorf("kubeconfig %s: cluster %q: certificate-authority: %w", path, clusterName, err)
		}
		if ca != nil {
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM(ca) {
				return nil, fmt.Errorf("kubeconfig %s: cluster %q: certificate-authority holds no certificate", path, clusterName)
			}
			rc.tls.RootCAs = pool
		}
	}
	if !found {
		return nil, fmt.Errorf("kubeconfig %s: no cluster %q", path, clusterName)
	}
	for _, u := range kc.Users {
		if u.Name != userName {
			continue
		}
		us := u.User
		if us.Exec != nil || us.AuthProvider != nil {
			return nil, fmt.Errorf("kubeconfig %s: user %q authenticates with a plugin (exec or auth-provider), which fit does not run: use a token, a client certificate, or fit inside the cluster", path, userName)
		}
		cert, err := pemOf(us.ClientCertData, file(us.ClientCert))
		if err != nil {
			return nil, fmt.Errorf("kubeconfig %s: user %q: client-certificate: %w", path, userName, err)
		}
		key, err := pemOf(us.ClientKeyData, file(us.ClientKey))
		if err != nil {
			return nil, fmt.Errorf("kubeconfig %s: user %q: client-key: %w", path, userName, err)
		}
		if cert != nil || key != nil {
			pair, err := tls.X509KeyPair(cert, key)
			if err != nil {
				return nil, fmt.Errorf("kubeconfig %s: user %q: %w", path, userName, err)
			}
			rc.tls.Certificates = []tls.Certificate{pair}
		}
		switch {
		case us.Token != "":
			rc.token = &token{static: us.Token}
		case us.TokenFile != "":
			rc.token = &token{file: file(us.TokenFile)}
		}
	}
	return rc, nil
}

// pemOf returns PEM given as base64 data, or read from a file, or nil
// when neither is given.
func pemOf(data, path string) ([]byte, error) {
	switch {
	case data != "":
		return base64.StdEncoding.DecodeString(data)
	case path != "":
		return os.ReadFile(path)
	}
	return nil, nil
}

func newClient(rc *restConfig, timeout time.Duration) *client {
	dialer := &net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
	tr := &http.Transport{
		DialContext:            dialer.DialContext,
		TLSClientConfig:        rc.tls,
		TLSHandshakeTimeout:    timeout,
		ResponseHeaderTimeout:  timeout,
		IdleConnTimeout:        90 * time.Second,
		MaxIdleConnsPerHost:    4,
		MaxResponseHeaderBytes: 64 << 10,
		ForceAttemptHTTP2:      true,
	}
	return &client{server: rc.server, hc: &http.Client{Transport: tr}, timeout: timeout, token: rc.token}
}

// get sends a GET for path with query, and returns the response if its
// status is 200. A 404 gives errNotFound, a 410 errGone.
func (c *client) get(ctx context.Context, path string, query url.Values) (*http.Response, error) {
	u := c.server + path
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if c.token != nil {
		t, err := c.token.get()
		if err != nil {
			return nil, fmt.Errorf("token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+t)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode == http.StatusOK {
		return resp, nil
	}
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))
	resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusNotFound:
		return nil, errNotFound
	case http.StatusGone:
		return nil, errGone
	}
	var status struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(msg, &status) == nil && status.Message != "" {
		msg = []byte(status.Message)
	}
	return nil, fmt.Errorf("GET %s: %s: %s", path, resp.Status, bytes.TrimSpace(msg))
}

// list lists every object at path, page by page, each within the request
// timeout, and returns them with the list's resource version. It stops
// once it has more than max objects, and says so.
func (c *client) list(ctx context.Context, path string, query url.Values, max int) (items []json.RawMessage, rv string, truncated bool, err error) {
	q := url.Values{"limit": {fmt.Sprint(pageSize)}}
	for k, v := range query {
		q[k] = v
	}
	for {
		var page struct {
			Metadata listMeta          `json:"metadata"`
			Items    []json.RawMessage `json:"items"`
		}
		if err := c.page(ctx, path, q, &page); err != nil {
			if errors.Is(err, errGone) {
				// The continue token expired: start over.
				q.Del("continue")
				items = items[:0]
				continue
			}
			return nil, "", false, err
		}
		items = append(items, page.Items...)
		rv = page.Metadata.ResourceVersion
		if len(items) > max {
			return items[:max], rv, true, nil
		}
		if page.Metadata.Continue == "" {
			return items, rv, false, nil
		}
		q.Set("continue", page.Metadata.Continue)
	}
}

func (c *client) page(ctx context.Context, path string, q url.Values, v any) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	resp, err := c.get(ctx, path, q)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes+1))
	if err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	if len(data) > maxPageBytes {
		return fmt.Errorf("GET %s: a page is larger than %d bytes", path, maxPageBytes)
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("GET %s: %w", path, err)
	}
	return nil
}

// watch opens a watch of path, narrowed by query, from the resource
// version rv, which lasts watchSeconds, until ctx ends, or until the
// server ends it.
func (c *client) watch(ctx context.Context, path string, query url.Values, rv string) (io.ReadCloser, error) {
	q := url.Values{
		"watch":               {"1"},
		"resourceVersion":     {rv},
		"allowWatchBookmarks": {"true"},
		"timeoutSeconds":      {fmt.Sprint(watchSeconds)},
	}
	for k, v := range query {
		q[k] = v
	}
	resp, err := c.get(ctx, path, q)
	if err != nil {
		return nil, err
	}
	return resp.Body, nil
}

// patchStatus sends a JSON merge patch to the status of the object at path
// (its path on the API server, without "/status"), within the request
// timeout. A 404 gives errNotFound, a 409 errConflict: the patch names the
// resource version it was made from, and the object changed since.
func (c *client) patchStatus(ctx context.Context, path string, patch []byte) error {
	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, c.server+path+"/status", bytes.NewReader(patch))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/merge-patch+json")
	req.Header.Set("Accept", "application/json")
	if c.token != nil {
		t, err := c.token.get()
		if err != nil {
			return fmt.Errorf("token: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+t)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		return errNotFound
	case http.StatusConflict:
		return errConflict
	}
	var status struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(msg, &status) == nil && status.Message != "" {
		msg = []byte(status.Message)
	}
	return fmt.Errorf("PATCH %s/status: %s: %s", path, resp.Status, bytes.TrimSpace(msg))
}
