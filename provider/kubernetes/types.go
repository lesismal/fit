package kubernetes

import "encoding/json"

// The fields the provider reads of the kinds it watches, as the API server
// writes them in JSON; everything else is left out.

type meta struct {
	Name              string            `json:"name"`
	Namespace         string            `json:"namespace,omitempty"`
	ResourceVersion   string            `json:"resourceVersion,omitempty"`
	Generation        int64             `json:"generation,omitempty"`
	CreationTimestamp string            `json:"creationTimestamp,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
	Annotations       map[string]string `json:"annotations,omitempty"`
}

// key is how a store keys an object: its namespace and name.
func (m *meta) key() string {
	if m.Namespace == "" {
		return m.Name
	}
	return m.Namespace + "/" + m.Name
}

type listMeta struct {
	ResourceVersion string `json:"resourceVersion"`
	Continue        string `json:"continue,omitempty"`
}

// Namespace is a core/v1 Namespace, for its labels.
type Namespace struct {
	Metadata meta `json:"metadata"`
}

// Condition is a metav1.Condition, as the Gateway API's statuses hold
// them.
type Condition struct {
	Type               string `json:"type"`
	Status             string `json:"status"`
	ObservedGeneration int64  `json:"observedGeneration,omitempty"`
	LastTransitionTime string `json:"lastTransitionTime"`
	Reason             string `json:"reason"`
	Message            string `json:"message"`
}

// GatewayClass is a gateway.networking.k8s.io/v1 GatewayClass.
type GatewayClass struct {
	Metadata meta `json:"metadata"`
	Spec     struct {
		ControllerName string `json:"controllerName"`
	} `json:"spec"`
	Status struct {
		Conditions []Condition `json:"conditions,omitempty"`
	} `json:"status"`
}

// Gateway is a gateway.networking.k8s.io/v1 Gateway.
type Gateway struct {
	Metadata meta `json:"metadata"`
	Spec     struct {
		GatewayClassName string     `json:"gatewayClassName"`
		Listeners        []Listener `json:"listeners"`
	} `json:"spec"`
	Status GatewayStatus `json:"status"`
}

// GatewayStatus is the part of a Gateway's status fit writes.
type GatewayStatus struct {
	Conditions []Condition      `json:"conditions,omitempty"`
	Listeners  []ListenerStatus `json:"listeners,omitempty"`
}

// ListenerStatus is the status of one of a Gateway's listeners.
type ListenerStatus struct {
	Name           string           `json:"name"`
	SupportedKinds []RouteGroupKind `json:"supportedKinds"`
	AttachedRoutes int              `json:"attachedRoutes"`
	Conditions     []Condition      `json:"conditions"`
}

// Listener is one of a Gateway's listeners.
type Listener struct {
	Name          string         `json:"name"`
	Hostname      *string        `json:"hostname,omitempty"`
	Port          int            `json:"port"`
	Protocol      string         `json:"protocol"`
	TLS           *ListenerTLS   `json:"tls,omitempty"`
	AllowedRoutes *AllowedRoutes `json:"allowedRoutes,omitempty"`
}

// ListenerTLS is how a listener serves TLS: terminated, with the
// certificates of Secrets, or passed through.
type ListenerTLS struct {
	Mode            *string     `json:"mode,omitempty"`
	CertificateRefs []SecretRef `json:"certificateRefs,omitempty"`
}

// SecretRef names a Secret, by default in the Gateway's namespace.
type SecretRef struct {
	Group     *string `json:"group,omitempty"`
	Kind      *string `json:"kind,omitempty"`
	Name      string  `json:"name"`
	Namespace *string `json:"namespace,omitempty"`
}

// RouteGroupKind is a kind of route.
type RouteGroupKind struct {
	Group *string `json:"group,omitempty"`
	Kind  string  `json:"kind"`
}

// AllowedRoutes says which routes may attach to a listener.
type AllowedRoutes struct {
	Namespaces *struct {
		From     string         `json:"from,omitempty"`
		Selector *LabelSelector `json:"selector,omitempty"`
	} `json:"namespaces,omitempty"`
	Kinds []RouteGroupKind `json:"kinds,omitempty"`
}

// LabelSelector is a metav1.LabelSelector.
type LabelSelector struct {
	MatchLabels      map[string]string `json:"matchLabels,omitempty"`
	MatchExpressions []struct {
		Key      string   `json:"key"`
		Operator string   `json:"operator"`
		Values   []string `json:"values,omitempty"`
	} `json:"matchExpressions,omitempty"`
}

// HTTPRoute is a gateway.networking.k8s.io/v1 HTTPRoute.
type HTTPRoute struct {
	Metadata meta `json:"metadata"`
	Spec     struct {
		ParentRefs []ParentRef     `json:"parentRefs,omitempty"`
		Hostnames  []string        `json:"hostnames,omitempty"`
		Rules      []HTTPRouteRule `json:"rules,omitempty"`
	} `json:"spec"`
	Status RouteStatus `json:"status"`
}

// RouteStatus is a route's status: an entry for each of its parents, each
// written by the controller of that parent. fit reads its own, and keeps the
// others' as they are.
type RouteStatus struct {
	Parents []json.RawMessage `json:"parents,omitempty"`
}

// RouteParentStatus is a route's status on one of its parents.
type RouteParentStatus struct {
	ParentRef      ParentRef   `json:"parentRef"`
	ControllerName string      `json:"controllerName"`
	Conditions     []Condition `json:"conditions"`
}

// GRPCRoute is a gateway.networking.k8s.io/v1 GRPCRoute.
type GRPCRoute struct {
	Metadata meta `json:"metadata"`
	Spec     struct {
		ParentRefs []ParentRef     `json:"parentRefs,omitempty"`
		Hostnames  []string        `json:"hostnames,omitempty"`
		Rules      []GRPCRouteRule `json:"rules,omitempty"`
	} `json:"spec"`
	Status RouteStatus `json:"status"`
}

// GRPCRouteRule is one of a GRPCRoute's rules. Its filters are those of an
// HTTPRoute's that gRPC has: header modifiers, mirrors and extensions.
type GRPCRouteRule struct {
	Matches     []GRPCRouteMatch  `json:"matches,omitempty"`
	Filters     []HTTPRouteFilter `json:"filters,omitempty"`
	BackendRefs []HTTPBackendRef  `json:"backendRefs,omitempty"`
}

// GRPCRouteMatch is what a gRPC request must match.
type GRPCRouteMatch struct {
	Method *struct {
		Type    *string `json:"type,omitempty"`
		Service *string `json:"service,omitempty"`
		Method  *string `json:"method,omitempty"`
	} `json:"method,omitempty"`
	Headers []HTTPMatchValue `json:"headers,omitempty"`
}

// L4Route is a gateway.networking.k8s.io TLSRoute, TCPRoute or UDPRoute,
// which have the same fields but a TLSRoute's host names: connections, or
// datagrams, are sent to their backends as they are.
type L4Route struct {
	Metadata meta `json:"metadata"`
	Spec     struct {
		ParentRefs []ParentRef `json:"parentRefs,omitempty"`
		Hostnames  []string    `json:"hostnames,omitempty"`
		Rules      []struct {
			BackendRefs []BackendRef `json:"backendRefs,omitempty"`
		} `json:"rules,omitempty"`
	} `json:"spec"`
	Status RouteStatus `json:"status"`
}

// ParentRef names the Gateway, and maybe the listener, a route attaches to.
type ParentRef struct {
	Group       *string `json:"group,omitempty"`
	Kind        *string `json:"kind,omitempty"`
	Namespace   *string `json:"namespace,omitempty"`
	Name        string  `json:"name"`
	SectionName *string `json:"sectionName,omitempty"`
	Port        *int    `json:"port,omitempty"`
}

// HTTPRouteRule is one of an HTTPRoute's rules.
type HTTPRouteRule struct {
	Matches     []HTTPRouteMatch  `json:"matches,omitempty"`
	Filters     []HTTPRouteFilter `json:"filters,omitempty"`
	BackendRefs []HTTPBackendRef  `json:"backendRefs,omitempty"`
}

// HTTPRouteMatch is what a request must match.
type HTTPRouteMatch struct {
	Path *struct {
		Type  *string `json:"type,omitempty"`
		Value *string `json:"value,omitempty"`
	} `json:"path,omitempty"`
	Headers     []HTTPMatchValue `json:"headers,omitempty"`
	QueryParams []HTTPMatchValue `json:"queryParams,omitempty"`
	Method      *string          `json:"method,omitempty"`
}

// HTTPMatchValue matches a header or a query parameter.
type HTTPMatchValue struct {
	Type  *string `json:"type,omitempty"`
	Name  string  `json:"name"`
	Value string  `json:"value"`
}

// HTTPRouteFilter changes requests or responses.
type HTTPRouteFilter struct {
	Type                   string               `json:"type"`
	RequestHeaderModifier  *HTTPHeaderFilter    `json:"requestHeaderModifier,omitempty"`
	ResponseHeaderModifier *HTTPHeaderFilter    `json:"responseHeaderModifier,omitempty"`
	RequestRedirect        *HTTPRequestRedirect `json:"requestRedirect,omitempty"`
	URLRewrite             *HTTPURLRewrite      `json:"urlRewrite,omitempty"`
	RequestMirror          *HTTPRequestMirror   `json:"requestMirror,omitempty"`
}

// HTTPHeaderFilter sets, adds and removes header fields.
type HTTPHeaderFilter struct {
	Set []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"set,omitempty"`
	Add []struct {
		Name  string `json:"name"`
		Value string `json:"value"`
	} `json:"add,omitempty"`
	Remove []string `json:"remove,omitempty"`
}

// HTTPPathModifier replaces a request's path.
type HTTPPathModifier struct {
	Type               string  `json:"type"`
	ReplaceFullPath    *string `json:"replaceFullPath,omitempty"`
	ReplacePrefixMatch *string `json:"replacePrefixMatch,omitempty"`
}

// HTTPRequestRedirect answers with a redirect.
type HTTPRequestRedirect struct {
	Scheme     *string           `json:"scheme,omitempty"`
	Hostname   *string           `json:"hostname,omitempty"`
	Path       *HTTPPathModifier `json:"path,omitempty"`
	Port       *int              `json:"port,omitempty"`
	StatusCode *int              `json:"statusCode,omitempty"`
}

// HTTPURLRewrite changes the request's host and path.
type HTTPURLRewrite struct {
	Hostname *string           `json:"hostname,omitempty"`
	Path     *HTTPPathModifier `json:"path,omitempty"`
}

// HTTPRequestMirror copies requests to a backend.
type HTTPRequestMirror struct {
	BackendRef BackendObjectRef `json:"backendRef"`
	Percent    *int             `json:"percent,omitempty"`
	Fraction   *struct {
		Numerator   int  `json:"numerator"`
		Denominator *int `json:"denominator,omitempty"`
	} `json:"fraction,omitempty"`
}

// BackendObjectRef names a backend: a Service's port.
type BackendObjectRef struct {
	Group     *string `json:"group,omitempty"`
	Kind      *string `json:"kind,omitempty"`
	Name      string  `json:"name"`
	Namespace *string `json:"namespace,omitempty"`
	Port      *int    `json:"port,omitempty"`
}

// BackendRef is a rule's backend, with its weight.
type BackendRef struct {
	BackendObjectRef
	Weight *int `json:"weight,omitempty"`
}

// HTTPBackendRef is an HTTP rule's backend, with its own filters.
type HTTPBackendRef struct {
	BackendRef
	Filters []HTTPRouteFilter `json:"filters,omitempty"`
}

// ReferenceGrant is a gateway.networking.k8s.io/v1beta1 ReferenceGrant:
// what in its namespace may be referred to from others.
type ReferenceGrant struct {
	Metadata meta `json:"metadata"`
	Spec     struct {
		From []struct {
			Group     string `json:"group"`
			Kind      string `json:"kind"`
			Namespace string `json:"namespace"`
		} `json:"from"`
		To []struct {
			Group string  `json:"group"`
			Kind  string  `json:"kind"`
			Name  *string `json:"name,omitempty"`
		} `json:"to"`
	} `json:"spec"`
}

// Secret is a core/v1 Secret of type kubernetes.io/tls, the certificate
// of an HTTPS listener.
type Secret struct {
	Metadata meta              `json:"metadata"`
	Type     string            `json:"type"`
	Data     map[string][]byte `json:"data,omitempty"`
}

// Service is a core/v1 Service.
type Service struct {
	Metadata meta `json:"metadata"`
	Spec     struct {
		Type         string        `json:"type,omitempty"`
		ClusterIP    string        `json:"clusterIP,omitempty"`
		ExternalName string        `json:"externalName,omitempty"`
		Ports        []ServicePort `json:"ports,omitempty"`
	} `json:"spec"`
}

// ServicePort is one of a Service's ports.
type ServicePort struct {
	Name        string  `json:"name,omitempty"`
	Protocol    string  `json:"protocol,omitempty"`
	Port        int     `json:"port"`
	AppProtocol *string `json:"appProtocol,omitempty"`
}

// EndpointSlice is a discovery.k8s.io/v1 EndpointSlice.
type EndpointSlice struct {
	Metadata    meta   `json:"metadata"`
	AddressType string `json:"addressType"`
	Endpoints   []struct {
		Addresses  []string `json:"addresses"`
		Conditions struct {
			Ready       *bool `json:"ready,omitempty"`
			Serving     *bool `json:"serving,omitempty"`
			Terminating *bool `json:"terminating,omitempty"`
		} `json:"conditions"`
	} `json:"endpoints"`
	Ports []struct {
		Name     *string `json:"name,omitempty"`
		Protocol *string `json:"protocol,omitempty"`
		Port     *int    `json:"port,omitempty"`
	} `json:"ports"`
}

// IngressClass is a networking.k8s.io/v1 IngressClass.
type IngressClass struct {
	Metadata meta `json:"metadata"`
	Spec     struct {
		Controller string `json:"controller"`
	} `json:"spec"`
}

// Ingress is a networking.k8s.io/v1 Ingress.
type Ingress struct {
	Metadata meta `json:"metadata"`
	Spec     struct {
		IngressClassName *string         `json:"ingressClassName,omitempty"`
		DefaultBackend   *IngressBackend `json:"defaultBackend,omitempty"`
		TLS              []struct {
			Hosts      []string `json:"hosts,omitempty"`
			SecretName string   `json:"secretName,omitempty"`
		} `json:"tls,omitempty"`
		Rules []IngressRule `json:"rules,omitempty"`
	} `json:"spec"`
}

// IngressRule is an Ingress's paths for a host, or for any.
type IngressRule struct {
	Host string `json:"host,omitempty"`
	HTTP *struct {
		Paths []IngressPath `json:"paths"`
	} `json:"http,omitempty"`
}

// IngressPath is a path of a rule and its backend.
type IngressPath struct {
	Path     string         `json:"path,omitempty"`
	PathType string         `json:"pathType"`
	Backend  IngressBackend `json:"backend"`
}

// IngressBackend is a Service's port, by number or name, or another
// resource.
type IngressBackend struct {
	Service *struct {
		Name string `json:"name"`
		Port struct {
			Name   string `json:"name,omitempty"`
			Number int    `json:"number,omitempty"`
		} `json:"port"`
	} `json:"service,omitempty"`
	Resource *struct {
		Kind string `json:"kind"`
		Name string `json:"name"`
	} `json:"resource,omitempty"`
}
