package config

import (
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Provider is a source of config other than the file and the Admin API:
// what it provides is merged into the config, by the rules of
// docs/decisions/009-provider-merging.md. Exactly one of its kinds is set.
type Provider struct {
	Name       string              `json:"name,omitempty" doc:"The name the provider goes by in the Admin API's /config/effective and in logs: lower-case letters, digits, '_', '.' and '-', unique among the providers. Its kind, as docker, when left out."`
	Docker     *DockerProvider     `json:"docker,omitempty" doc:"Routes, upstreams and services from the labels of running Docker containers."`
	Consul     *ConsulProvider     `json:"consul,omitempty" doc:"Routes, upstreams and services from the tags of the services in a Consul catalog, sending to their instances whose health checks pass."`
	ConsulKV   *ConsulKVProvider   `json:"consul_kv,omitempty" doc:"Listeners, routes, upstreams and services from the keys under a prefix in Consul's KV store, each holding a JSON object of them."`
	Etcd       *EtcdProvider       `json:"etcd,omitempty" doc:"Listeners, routes, upstreams and services from the keys under a prefix in etcd, each holding a JSON object of them."`
	Kubernetes *KubernetesProvider `json:"kubernetes,omitempty" doc:"Listeners, routes, services and upstreams from a Kubernetes cluster's Gateway API resources: the Gateways of the GatewayClasses whose controllerName is fit's, and their HTTPRoutes, GRPCRoutes, TLSRoutes, TCPRoutes and UDPRoutes, whose status fit writes back."`

	At Value `json:"-" fit:"self"`
}

// Kind returns which kind of provider p is, or "" when none is set.
func (p *Provider) Kind() string {
	switch {
	case p.Docker != nil:
		return "docker"
	case p.Consul != nil:
		return "consul"
	case p.ConsulKV != nil:
		return "consul_kv"
	case p.Etcd != nil:
		return "etcd"
	case p.Kubernetes != nil:
		return "kubernetes"
	}
	return ""
}

// ID returns p's name, its kind when it has none.
func (p *Provider) ID() string {
	if p.Name != "" {
		return p.Name
	}
	return p.Kind()
}

// The defaults of DockerProvider.
const (
	DefaultDockerEndpoint      = "unix:///var/run/docker.sock"
	DefaultDockerLabelPrefix   = "fit"
	DefaultDockerMaxContainers = 1000
)

// The defaults of DockerProvider's durations.
var (
	DefaultDockerRefreshInterval = Duration(30 * time.Second)
	DefaultDockerRequestTimeout  = Duration(10 * time.Second)
)

// DockerProvider turns the labels of running containers into routes,
// upstreams and services. It follows the daemon's events, and lists the
// containers again at each change and every refresh_interval.
type DockerProvider struct {
	Endpoint          string   `json:"endpoint,omitempty" default:"\"unix:///var/run/docker.sock\"" doc:"Where the Docker daemon's API is: unix:// and a socket's path, or tcp:// and a host and port, as of a socket proxy, in plain HTTP. unix:///var/run/docker.sock when left out."`
	ExposeAll         bool     `json:"expose_all,omitempty" doc:"Take every running container with routes or services in its labels; only those labelled <label_prefix>.enable=true when left out or false. A container labelled <label_prefix>.enable=false is never taken."`
	Network           string   `json:"network,omitempty" doc:"The Docker network whose address of a container's is used, when the container is on several and has no <label_prefix>.network label; the first by name when left out."`
	UsePublishedPorts bool     `json:"use_published_ports,omitempty" doc:"Send to the port a container's port is published on, on the host, rather than to the container's own address: for a fit outside Docker that cannot reach containers' addresses, as on Docker Desktop."`
	LabelPrefix       string   `json:"label_prefix,omitempty" default:"\"fit\"" doc:"What the labels read start with, as in fit.routes.web.host. fit when left out."`
	MaxContainers     int      `json:"max_containers,omitempty" min:"0" default:"1000" doc:"The most containers taken; past it, the rest, by name, are left out and reported. 1000 when left out or 0."`
	RefreshInterval   Duration `json:"refresh_interval,omitempty" default:"\"30s\"" doc:"How often the containers are listed again whether or not an event came, in case one was missed. 30s when left out."`
	RequestTimeout    Duration `json:"request_timeout,omitempty" default:"\"10s\"" doc:"How long listing the containers may take, connecting included. 10s when left out."`

	At Value `json:"-" fit:"self"`
}

// Prefix, Containers, Refresh and Timeout return d's settings, their
// defaults when left out.
func (d *DockerProvider) Prefix() string {
	if d.LabelPrefix == "" {
		return DefaultDockerLabelPrefix
	}
	return d.LabelPrefix
}

func (d *DockerProvider) Containers() int {
	return int(orDefault(int64(d.MaxContainers), DefaultDockerMaxContainers))
}

func (d *DockerProvider) Refresh() time.Duration {
	if d.RefreshInterval <= 0 {
		return DefaultDockerRefreshInterval.Std()
	}
	return d.RefreshInterval.Std()
}

func (d *DockerProvider) Timeout() time.Duration {
	if d.RequestTimeout <= 0 {
		return DefaultDockerRequestTimeout.Std()
	}
	return d.RequestTimeout.Std()
}

// Dial returns the network and address the daemon's API is at.
func (d *DockerProvider) Dial() (network, address string) {
	ep := d.Endpoint
	if ep == "" {
		ep = DefaultDockerEndpoint
	}
	if path, ok := strings.CutPrefix(ep, "unix://"); ok {
		return "unix", path
	}
	if hostport, ok := strings.CutPrefix(ep, "tcp://"); ok {
		return "tcp", hostport
	}
	return "", ep
}

// providerPattern is what a provider's name looks like.
var providerPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,62}$`)

// baseSources are the names of the sources of the base config, which no
// provider may take.
var baseSources = []string{"file", "admin", "rollback", "certificates"}

func checkProviders(ps []Provider, add func(Value, string, ...any)) {
	seen := map[string]bool{}
	for _, p := range ps {
		kinds := 0
		for _, set := range []bool{p.Docker != nil, p.Consul != nil, p.ConsulKV != nil, p.Etcd != nil, p.Kubernetes != nil} {
			if set {
				kinds++
			}
		}
		if kinds != 1 {
			add(p.At, "set exactly one kind of provider: docker, consul, consul_kv, etcd or kubernetes")
			continue
		}
		id := p.ID()
		switch {
		case !providerPattern.MatchString(id):
			add(p.At.Field("name"), "%q is not a valid name: use lower-case letters, digits, '_', '.' and '-', starting with a letter or digit, at most 63", id)
		case slices.Contains(baseSources, id):
			add(p.At.Field("name"), "%q names a source of the base config: pick another name", id)
		case seen[id]:
			add(p.At.Field("name"), "another provider is already named %q: give one a name", id)
		}
		seen[id] = true
		if d := p.Docker; d != nil {
			network, address := d.Dial()
			switch {
			case network == "":
				add(d.At.Field("endpoint"), "%q is neither unix://<path> nor tcp://<host>:<port>", d.Endpoint)
			case network == "unix" && address == "":
				add(d.At.Field("endpoint"), "%q has no path", d.Endpoint)
			case network == "tcp":
				if _, _, err := net.SplitHostPort(address); err != nil {
					add(d.At.Field("endpoint"), "%q has no host and port", d.Endpoint)
				}
			}
			if d.LabelPrefix != "" && !namePattern.MatchString(d.LabelPrefix) {
				add(d.At.Field("label_prefix"), "%q is not a valid prefix: use letters, digits, '_', '.' and '-', starting with a letter or digit", d.LabelPrefix)
			}
		}
		if c := p.Consul; c != nil {
			checkEndpoint(c.At.Field("endpoint"), c.Endpoint, add)
			checkTLSFiles(c.At, c.CertFile, c.KeyFile, add)
			if c.TagPrefix != "" && !namePattern.MatchString(c.TagPrefix) {
				add(c.At.Field("tag_prefix"), "%q is not a valid prefix: use letters, digits, '_', '.' and '-', starting with a letter or digit", c.TagPrefix)
			}
		}
		if c := p.ConsulKV; c != nil {
			checkEndpoint(c.At.Field("endpoint"), c.Endpoint, add)
			checkTLSFiles(c.At, c.CertFile, c.KeyFile, add)
		}
		if e := p.Etcd; e != nil {
			for i, ep := range e.Endpoints {
				checkEndpoint(e.At.Field("endpoints").Index(i), ep, add)
			}
			checkTLSFiles(e.At, e.CertFile, e.KeyFile, add)
			switch {
			case e.Username != "" && e.PasswordFile == "":
				add(e.At.Field("username"), "username needs password_file")
			case e.PasswordFile != "" && e.Username == "":
				add(e.At.Field("password_file"), "password_file needs username")
			}
		}
		if k := p.Kubernetes; k != nil {
			if k.ListenHost != "" && net.ParseIP(k.ListenHost) == nil && k.ListenHost != "localhost" {
				add(k.At.Field("listen_host"), "%q is not an IP address or localhost", k.ListenHost)
			}
			if k.ControllerName != "" && !controllerPattern.MatchString(k.ControllerName) {
				add(k.At.Field("controller_name"), "%q is not a domain-prefixed path, as example.net/gateway-controller", k.ControllerName)
			}
			for i, a := range k.Addresses {
				if net.ParseIP(a) == nil && (strings.HasPrefix(a, "*.") || checkHost(a) != "") {
					add(k.At.Field("addresses").Index(i), "%q is not an IP address or host name", a)
				}
			}
			if in := k.Ingress; in != nil {
				for _, f := range []struct {
					name string
					port int
				}{{"http_port", in.HTTPPort}, {"https_port", in.HTTPSPort}} {
					if f.port < 0 || f.port > 65535 {
						add(in.At.Field(f.name), "%d is not a port", f.port)
					}
				}
				if in.HTTP() == in.HTTPS() {
					add(in.At.Field("https_port"), "port %d serves HTTP already: HTTPS needs a port of its own", in.HTTPS())
				}
			}
		}
	}
}

// checkEndpoint checks an endpoint of an HTTP API, http:// or https://
// and a host and port; empty is the default.
func checkEndpoint(at Value, ep string, add func(Value, string, ...any)) {
	if ep == "" {
		return
	}
	rest, ok := strings.CutPrefix(ep, "http://")
	if !ok {
		rest, ok = strings.CutPrefix(ep, "https://")
	}
	if !ok {
		add(at, "%q is neither http://<host>:<port> nor https://<host>:<port>", ep)
		return
	}
	_, port, err := net.SplitHostPort(rest)
	if n, perr := strconv.Atoi(port); err != nil || perr != nil || n < 1 || n > 65535 || strings.ContainsAny(rest, "/?#") {
		add(at, "%q is not a host and port alone", ep)
	}
}

func checkTLSFiles(at Value, cert, key string, add func(Value, string, ...any)) {
	switch {
	case cert != "" && key == "":
		add(at.Field("cert_file"), "cert_file needs key_file")
	case key != "" && cert == "":
		add(at.Field("key_file"), "key_file needs cert_file")
	}
}

// The defaults of ConsulProvider, ConsulKVProvider and EtcdProvider.
const (
	DefaultConsulEndpoint    = "http://127.0.0.1:8500"
	DefaultConsulTagPrefix   = "fit"
	DefaultConsulMaxServices = 1000
	DefaultEtcdEndpoint      = "http://127.0.0.1:2379"
	DefaultKVPrefix          = "fit/"
	DefaultKVMaxKeys         = 1000
)

// The defaults of their durations.
var (
	DefaultWatchRefreshInterval = Duration(30 * time.Second)
	DefaultWatchRequestTimeout  = Duration(10 * time.Second)
)

func refreshOr(d Duration) time.Duration {
	if d <= 0 {
		return DefaultWatchRefreshInterval.Std()
	}
	return d.Std()
}

func timeoutOr(d Duration) time.Duration {
	if d <= 0 {
		return DefaultWatchRequestTimeout.Std()
	}
	return d.Std()
}

func kvPrefixOr(p string) string {
	if p == "" {
		return DefaultKVPrefix
	}
	return p
}

// ConsulProvider turns the services of a Consul catalog into upstreams, of
// their instances whose health checks pass, and the routes and services
// their tags write, as a container's labels do for the Docker provider:
// <tag_prefix>.routes.web.host=example.com, say. It follows the catalog
// and the health checks with blocking queries.
type ConsulProvider struct {
	Endpoint        string   `json:"endpoint,omitempty" default:"\"http://127.0.0.1:8500\"" doc:"Where Consul's HTTP API is, as of the local agent: http:// or https:// and a host and port. http://127.0.0.1:8500 when left out."`
	Datacenter      string   `json:"datacenter,omitempty" doc:"The datacenter whose catalog is read; the agent's when left out."`
	TokenFile       string   `json:"token_file,omitempty" doc:"A file holding the ACL token to send, read each time a watch starts; none when left out. The token needs to read the services and nodes served."`
	CAFile          string   `json:"ca_file,omitempty" doc:"A PEM file with the CAs Consul's certificate is verified with, over https; the system's when left out."`
	CertFile        string   `json:"cert_file,omitempty" doc:"A PEM file with the client certificate sent over https, as Consul's verify_incoming asks for; with key_file."`
	KeyFile         string   `json:"key_file,omitempty" doc:"A PEM file with cert_file's private key."`
	ExposeAll       bool     `json:"expose_all,omitempty" doc:"Take every service in the catalog; only those tagged <tag_prefix>.enable=true when left out or false. An instance tagged <tag_prefix>.enable=false is never taken."`
	TagPrefix       string   `json:"tag_prefix,omitempty" default:"\"fit\"" doc:"What the tags read start with, as in fit.routes.web.host=example.com; tags without '=' are passed over. Besides the routes, services and upstream settings a Docker container's labels may set, <tag_prefix>.name names the upstream, the service's name when left out. fit when left out."`
	MaxServices     int      `json:"max_services,omitempty" min:"0" default:"1000" doc:"The most services taken; past it, the rest, by name, are left out and reported. 1000 when left out or 0."`
	RefreshInterval Duration `json:"refresh_interval,omitempty" default:"\"30s\"" doc:"How often the catalog is read again whether or not it changed. 30s when left out."`
	RequestTimeout  Duration `json:"request_timeout,omitempty" default:"\"10s\"" doc:"How long each request to Consul may take, connecting included, beyond the five minutes a blocking query waits for a change. 10s when left out."`

	At Value `json:"-" fit:"self"`
}

// URL, Prefix, Services, Refresh and Timeout return c's settings, their
// defaults when left out.
func (c *ConsulProvider) URL() string {
	if c.Endpoint == "" {
		return DefaultConsulEndpoint
	}
	return c.Endpoint
}

func (c *ConsulProvider) Prefix() string {
	if c.TagPrefix == "" {
		return DefaultConsulTagPrefix
	}
	return c.TagPrefix
}

func (c *ConsulProvider) Services() int {
	return int(orDefault(int64(c.MaxServices), DefaultConsulMaxServices))
}

func (c *ConsulProvider) Refresh() time.Duration { return refreshOr(c.RefreshInterval) }

func (c *ConsulProvider) Timeout() time.Duration { return timeoutOr(c.RequestTimeout) }

// ConsulKVProvider reads the keys under a prefix in Consul's KV store, each
// holding a JSON object of listeners, routes, upstreams and services, and
// provides them together, in the order of their keys. It follows them with
// blocking queries.
type ConsulKVProvider struct {
	Endpoint        string   `json:"endpoint,omitempty" default:"\"http://127.0.0.1:8500\"" doc:"Where Consul's HTTP API is, as of the local agent: http:// or https:// and a host and port. http://127.0.0.1:8500 when left out."`
	Datacenter      string   `json:"datacenter,omitempty" doc:"The datacenter whose KV store is read; the agent's when left out."`
	TokenFile       string   `json:"token_file,omitempty" doc:"A file holding the ACL token to send, read each time a watch starts; none when left out. The token needs to read the keys under prefix."`
	CAFile          string   `json:"ca_file,omitempty" doc:"A PEM file with the CAs Consul's certificate is verified with, over https; the system's when left out."`
	CertFile        string   `json:"cert_file,omitempty" doc:"A PEM file with the client certificate sent over https, as Consul's verify_incoming asks for; with key_file."`
	KeyFile         string   `json:"key_file,omitempty" doc:"A PEM file with cert_file's private key."`
	Prefix          string   `json:"prefix,omitempty" default:"\"fit/\"" doc:"The keys read are those starting with it. Each holds a JSON object of listeners, routes, upstreams and services, as a config holds them; keys ending in '/' and empty ones are passed over, and a key whose value is not such an object is left out and reported. fit/ when left out."`
	MaxKeys         int      `json:"max_keys,omitempty" min:"0" default:"1000" doc:"The most keys taken; past it, the rest, by key, are left out and reported. 1000 when left out or 0."`
	RefreshInterval Duration `json:"refresh_interval,omitempty" default:"\"30s\"" doc:"How often what the keys provide is sent again whether or not it changed, so that a part refused because of the rest of the config is tried again. 30s when left out."`
	RequestTimeout  Duration `json:"request_timeout,omitempty" default:"\"10s\"" doc:"How long each request to Consul may take, connecting included, beyond the five minutes a blocking query waits for a change. 10s when left out."`

	At Value `json:"-" fit:"self"`
}

// URL, KeyPrefix, Keys, Refresh and Timeout return c's settings, their
// defaults when left out.
func (c *ConsulKVProvider) URL() string {
	if c.Endpoint == "" {
		return DefaultConsulEndpoint
	}
	return c.Endpoint
}

func (c *ConsulKVProvider) KeyPrefix() string { return kvPrefixOr(c.Prefix) }

func (c *ConsulKVProvider) Keys() int { return int(orDefault(int64(c.MaxKeys), DefaultKVMaxKeys)) }

func (c *ConsulKVProvider) Refresh() time.Duration { return refreshOr(c.RefreshInterval) }

func (c *ConsulKVProvider) Timeout() time.Duration { return timeoutOr(c.RequestTimeout) }

// EtcdProvider reads the keys under a prefix in etcd, each holding a JSON
// object of listeners, routes, upstreams and services, and provides them
// together, in the order of their keys. It follows them with a watch,
// through etcd's JSON gateway to its v3 API.
type EtcdProvider struct {
	Endpoints       []string `json:"endpoints,omitempty" default:"[\"http://127.0.0.1:2379\"]" doc:"Where etcd's client API is: http:// or https:// and a host and port, for each member to try, in turn when one fails. http://127.0.0.1:2379 when left out."`
	Username        string   `json:"username,omitempty" doc:"The user to authenticate as, with password_file, when etcd's authentication is on; none when left out."`
	PasswordFile    string   `json:"password_file,omitempty" doc:"A file holding username's password, read each time a watch starts."`
	CAFile          string   `json:"ca_file,omitempty" doc:"A PEM file with the CAs etcd's certificate is verified with, over https; the system's when left out."`
	CertFile        string   `json:"cert_file,omitempty" doc:"A PEM file with the client certificate sent over https, as etcd's client-cert-auth asks for; with key_file."`
	KeyFile         string   `json:"key_file,omitempty" doc:"A PEM file with cert_file's private key."`
	Prefix          string   `json:"prefix,omitempty" default:"\"fit/\"" doc:"The keys read are those starting with it. Each holds a JSON object of listeners, routes, upstreams and services, as a config holds them; keys ending in '/' and empty ones are passed over, and a key whose value is not such an object is left out and reported. fit/ when left out."`
	MaxKeys         int      `json:"max_keys,omitempty" min:"0" default:"1000" doc:"The most keys taken; past it, the rest, by key, are left out and reported. 1000 when left out or 0."`
	RefreshInterval Duration `json:"refresh_interval,omitempty" default:"\"30s\"" doc:"How often the keys are read again whether or not a watch event came. 30s when left out."`
	RequestTimeout  Duration `json:"request_timeout,omitempty" default:"\"10s\"" doc:"How long each request to etcd may take, connecting included, and a watch to start; a watch then lasts until it fails. 10s when left out."`

	At Value `json:"-" fit:"self"`
}

// URLs, KeyPrefix, Keys, Refresh and Timeout return e's settings, their
// defaults when left out.
func (e *EtcdProvider) URLs() []string {
	if len(e.Endpoints) == 0 {
		return []string{DefaultEtcdEndpoint}
	}
	return e.Endpoints
}

func (e *EtcdProvider) KeyPrefix() string { return kvPrefixOr(e.Prefix) }

func (e *EtcdProvider) Keys() int { return int(orDefault(int64(e.MaxKeys), DefaultKVMaxKeys)) }

func (e *EtcdProvider) Refresh() time.Duration { return refreshOr(e.RefreshInterval) }

func (e *EtcdProvider) Timeout() time.Duration { return timeoutOr(e.RequestTimeout) }

// The defaults of KubernetesProvider.
const (
	DefaultKubernetesControllerName = "lesismal.github.io/fit"
	DefaultKubernetesMaxObjects     = 10000
	KubernetesBackendsEndpoints     = "endpoints"
	KubernetesBackendsClusterIP     = "cluster_ip"
)

// The default of KubernetesProvider.RequestTimeout.
var DefaultKubernetesRequestTimeout = Duration(10 * time.Second)

// controllerPattern is what a Gateway API controller name looks like: a
// domain, then a path.
var controllerPattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/[A-Za-z0-9/\-._~%!$&'()*+,;=:]+$`)

// KubernetesProvider follows a cluster's Gateway API resources: each
// listener of a Gateway of fit's GatewayClasses is a listener of fit's on
// its port, each HTTPRoute and GRPCRoute attached to one is a set of
// routes, and each TLSRoute, TCPRoute and UDPRoute a service, to upstreams
// of the Services they name. With ingress, it serves Ingresses as well.
type KubernetesProvider struct {
	Kubeconfig      string             `json:"kubeconfig,omitempty" doc:"A kubeconfig file to reach the cluster with: its server, CA, client certificate, token or token file. When left out, the pod's service account, inside the cluster, or else $KUBECONFIG or ~/.kube/config. Exec and auth-provider plugins are not supported."`
	Context         string             `json:"context,omitempty" doc:"The kubeconfig's context to use; its current-context when left out."`
	ControllerName  string             `json:"controller_name,omitempty" default:"\"lesismal.github.io/fit\"" doc:"The controllerName of the GatewayClasses whose Gateways fit serves. lesismal.github.io/fit when left out."`
	Namespaces      []string           `json:"namespaces,omitempty" doc:"The namespaces whose Gateways and routes are served; every namespace when left out. Resources are watched across the cluster either way, so fit's account needs to list and watch them cluster-wide, Secrets of type kubernetes.io/tls among them, and to patch the status of GatewayClasses, Gateways and routes."`
	ListenHost      string             `json:"listen_host,omitempty" doc:"The IP address Gateway listeners are bound on, each on its port; every address when left out."`
	Backends        string             `json:"backends,omitempty" enum:"endpoints,cluster_ip" default:"\"endpoints\"" doc:"Where requests to a Service go: endpoints, to its ready endpoints from its EndpointSlices, balanced by fit; or cluster_ip, to its cluster IP, balanced by the cluster. endpoints when left out."`
	MaxObjects      int                `json:"max_objects,omitempty" min:"0" default:"10000" doc:"The most objects of each kind kept: past it, the rest are left out and reported. 10000 when left out or 0."`
	RequestTimeout  Duration           `json:"request_timeout,omitempty" default:"\"10s\"" doc:"How long each request to the API server may take, but watches, which last five minutes each. 10s when left out."`
	Addresses       []string           `json:"addresses,omitempty" doc:"The addresses where clients reach fit's Gateways, IP addresses or host names, written into each one's status.addresses, as clients of the Gateway API and its conformance tests read them. None are written when left out, and those there are kept."`
	Ingress         *KubernetesIngress `json:"ingress,omitempty" doc:"Serve Ingresses too: those whose ingressClassName, or kubernetes.io/ingress.class annotation, names an IngressClass whose controller is controller_name, and those naming none when such a class is the default. Their rules are served on http_port, and on https_port for the hosts of their tls, with the certificates of its Secrets. fit's account then needs to list and watch Ingresses and IngressClasses. No Ingresses when left out."`
	CertificatesDir string             `json:"certificates_dir,omitempty" doc:"The directory the certificates of HTTPS listeners are written to, from the Secrets they name, for fit's TLS to load: each Secret's chain and key in one file only fit's user may read, named for the Secret and a hash of its contents. Files of that form that are no longer named are removed. A directory of fit's own in the system's temporary directory, removed when fit stops, when left out."`

	At Value `json:"-" fit:"self"`
}

// KubernetesIngress is where Ingresses are served.
type KubernetesIngress struct {
	HTTPPort  int `json:"http_port,omitempty" default:"80" doc:"The port Ingresses' rules are served on over HTTP. 80 when left out or 0."`
	HTTPSPort int `json:"https_port,omitempty" default:"443" doc:"The port the hosts of Ingresses' tls are served on over HTTPS. 443 when left out or 0."`

	At Value `json:"-" fit:"self"`
}

// HTTP and HTTPS return the ports Ingresses are served on.
func (k *KubernetesIngress) HTTP() int { return int(orDefault(int64(k.HTTPPort), 80)) }

func (k *KubernetesIngress) HTTPS() int { return int(orDefault(int64(k.HTTPSPort), 443)) }

// Controller, Objects, Timeout and BackendMode return k's settings, their
// defaults when left out.
func (k *KubernetesProvider) Controller() string {
	if k.ControllerName == "" {
		return DefaultKubernetesControllerName
	}
	return k.ControllerName
}

func (k *KubernetesProvider) Objects() int {
	return int(orDefault(int64(k.MaxObjects), DefaultKubernetesMaxObjects))
}

func (k *KubernetesProvider) Timeout() time.Duration {
	if k.RequestTimeout <= 0 {
		return DefaultKubernetesRequestTimeout.Std()
	}
	return k.RequestTimeout.Std()
}

func (k *KubernetesProvider) BackendMode() string {
	if k.Backends == "" {
		return KubernetesBackendsEndpoints
	}
	return k.Backends
}
